// Package inspector reads schema and row data out of the databases an
// ensemble.yaml stack provisions (postgres, mysql, dynamodb) for the
// dashboard's entity pages, and polls for changes so the dashboard can
// live-update.
//
// Drivers are registered under the same names used in ensemble.yaml's
// `databases:` map. This package also provides SQLRunner, the concrete
// implementation of orchestrator.SQLRunner's seed-SQL seam (see
// sqlrunner.go).
package inspector

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Column describes one column of a Table.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}

// Table describes one table/collection a Driver exposes.
type Table struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
}

// Driver is one database's read-only inspection surface: schema, paged
// rows, and a cheap fingerprint used to detect row-level changes without
// re-reading the whole table.
type Driver interface {
	// Tables lists every inspectable table/collection.
	Tables(ctx context.Context) ([]Table, error)
	// Rows returns up to limit rows of table, skipping the first offset.
	Rows(ctx context.Context, table string, limit, offset int) ([]map[string]any, error)
	// Fingerprint returns a cheap token that changes whenever table's data
	// changes (e.g. "count=N;max=PK" for SQL tables, an item-count-derived
	// token for dynamo). It is compared byte-for-byte across polls — it
	// need not be human-meaningful, only stable when nothing changed and
	// different when something did.
	Fingerprint(ctx context.Context, table string) (string, error)
}

// ChangeEvent reports that DB.Table's fingerprint changed between two
// consecutive polls.
type ChangeEvent struct {
	DB    string
	Table string
	At    time.Time
}

// Inspector is a name -> Driver registry plus a poll-based change-stream
// engine (Watch). The registry itself is just bookkeeping: Schema/Rows are
// thin pass-throughs to the named Driver.
type Inspector struct {
	mu      sync.Mutex
	drivers map[string]Driver
}

// New returns an empty Inspector; call Register to add drivers.
func New() *Inspector {
	return &Inspector{drivers: map[string]Driver{}}
}

// Register associates name (an ensemble.yaml database name) with d. A
// second Register under the same name replaces the first.
func (i *Inspector) Register(name string, d Driver) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.drivers[name] = d
}

// driver returns db's registered Driver, if any.
func (i *Inspector) driver(db string) (Driver, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	d, ok := i.drivers[db]
	return d, ok
}

// Has reports whether db has a registered Driver — the server package's
// GET /api/databases uses this to compute cfg.Databases ∩ registered
// drivers without issuing a live Schema call just to probe membership.
func (i *Inspector) Has(db string) bool {
	_, ok := i.driver(db)
	return ok
}

// snapshot returns a stable copy of the current name -> Driver registry,
// sorted by name, for callers (Watch's poller) that must iterate outside
// the registry lock.
func (i *Inspector) snapshot() []struct {
	name string
	d    Driver
} {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]struct {
		name string
		d    Driver
	}, 0, len(i.drivers))
	for name, d := range i.drivers {
		out = append(out, struct {
			name string
			d    Driver
		}{name, d})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].name < out[b].name })
	return out
}

// Schema returns db's tables (and columns), by way of its registered
// Driver.
func (i *Inspector) Schema(ctx context.Context, db string) ([]Table, error) {
	d, ok := i.driver(db)
	if !ok {
		return nil, fmt.Errorf("inspector: database %q not registered", db)
	}
	return d.Tables(ctx)
}

// Rows returns up to limit rows of db.table, skipping offset, by way of
// db's registered Driver.
func (i *Inspector) Rows(ctx context.Context, db, table string, limit, offset int) ([]map[string]any, error) {
	d, ok := i.driver(db)
	if !ok {
		return nil, fmt.Errorf("inspector: database %q not registered", db)
	}
	return d.Rows(ctx, table, limit, offset)
}

// pollTimeout bounds each per-table Fingerprint call the poller issues, so
// one unreachable driver can't wedge the whole poll loop indefinitely.
const pollTimeout = 10 * time.Second

// Watch starts a background poller that, every interval, lists every
// registered database's tables and compares each one's Fingerprint against
// the value observed on the previous poll. A change (fingerprint differs
// from the previous poll's value for that DB.Table) is delivered as a
// ChangeEvent on the returned channel.
//
// The very first poll for a given DB.Table only establishes the baseline —
// it never fires an event, since there is nothing to compare against yet.
// An unchanged fingerprint across any number of subsequent polls never
// fires a repeat event (dedup): an event fires exactly once per actual
// change, the tick it's first observed on.
//
// This is the poller tier only (snapshot-diff around GUI mutations is
// deferred — see the design doc's Phase 3 entity-pages task).
//
// The returned func stops the poller and closes the channel; it is safe to
// call more than once.
func (i *Inspector) Watch(interval time.Duration) (<-chan ChangeEvent, func()) {
	events := make(chan ChangeEvent)
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)

		last := map[string]string{} // "db\x00table" -> last-seen fingerprint

		poll := func() bool {
			for _, entry := range i.snapshot() {
				ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
				tables, err := entry.d.Tables(ctx)
				cancel()
				if err != nil {
					continue
				}
				for _, tbl := range tables {
					ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
					fp, err := entry.d.Fingerprint(ctx, tbl.Name)
					cancel()
					if err != nil {
						continue
					}

					key := entry.name + "\x00" + tbl.Name
					prev, seen := last[key]
					last[key] = fp
					if !seen || prev == fp {
						continue
					}

					select {
					case events <- ChangeEvent{DB: entry.name, Table: tbl.Name, At: time.Now()}:
					case <-stopCh:
						return false
					}
				}
			}
			return true
		}

		if !poll() {
			return
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				if !poll() {
					return
				}
			}
		}
	}()

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			close(stopCh)
			<-doneCh
			close(events)
		})
	}
	return events, stop
}

// orderColumnFor picks the column a SQL driver's Rows query should ORDER BY
// for deterministic paging: the table's single-column primary key when it
// has one (pk/hasPK, as returned by each driver's primaryKeyColumn), else
// the first column by ordinal position, else "" (nothing to order by — a
// table with no columns). Pure and DB-independent so it's unit-testable
// without a live connection; LIMIT/OFFSET is unordered in both postgres
// and mysql without an ORDER BY, so paging would otherwise
// non-deterministically duplicate or skip rows across pages.
func orderColumnFor(pk string, hasPK bool, cols []Column) string {
	if hasPK {
		return pk
	}
	if len(cols) == 0 {
		return ""
	}
	return cols[0].Name
}

// scanRowsToMaps drains rows into one map[string]any per row, keyed by
// column name. Shared by the postgres and mysql drivers, both of which
// query through database/sql. []byte values are converted to string (both
// drivers otherwise return raw bytes for text-ish columns, which is rarely
// what a JSON-facing caller wants).
func scanRowsToMaps(rows *sql.Rows) ([]map[string]any, error) {
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("inspector: columns: %w", err)
	}

	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("inspector: scan: %w", err)
		}

		row := make(map[string]any, len(cols))
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[c] = v
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspector: rows: %w", err)
	}
	return out, nil
}
