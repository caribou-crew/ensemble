package inspector

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeDriver is a scripted Driver whose Fingerprint return value can be
// mutated by the test at will, so the change-stream engine can be exercised
// without a real database.
type fakeDriver struct {
	mu     sync.Mutex
	tables []Table
	fps    map[string]string
	rows   map[string][]map[string]any
}

func newFakeDriver(tableNames ...string) *fakeDriver {
	f := &fakeDriver{fps: map[string]string{}, rows: map[string][]map[string]any{}}
	for _, n := range tableNames {
		f.tables = append(f.tables, Table{Name: n})
		f.fps[n] = "v0"
	}
	return f
}

func (f *fakeDriver) Tables(ctx context.Context) ([]Table, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Table, len(f.tables))
	copy(out, f.tables)
	return out, nil
}

func (f *fakeDriver) Rows(ctx context.Context, table string, limit, offset int) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[table], nil
}

func (f *fakeDriver) Fingerprint(ctx context.Context, table string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fps[table], nil
}

func (f *fakeDriver) setFingerprint(table, fp string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fps[table] = fp
}

// TestOrderColumnForPrefersPrimaryKeyThenFirstColumn guards final-review
// finding #16 (promoted from task 2.5's deferred-minors list): the SQL
// drivers' Rows queries did LIMIT/OFFSET with no ORDER BY, which is
// unordered in both postgres and mysql — so entity-page pagination could
// non-deterministically duplicate or skip rows across pages. orderColumnFor
// is the pure (DB-independent) piece of that decision: a single-column
// primary key when the table has one, else the first column by ordinal
// position, else "" (no ORDER BY possible — a table with no columns).
// Unit-testable without a live database; the actual query wiring is
// covered by the env-gated live-DB integration tests.
func TestOrderColumnForPrefersPrimaryKeyThenFirstColumn(t *testing.T) {
	cases := []struct {
		name  string
		pk    string
		hasPK bool
		cols  []Column
		want  string
	}{
		{"pk wins over columns", "id", true, []Column{{Name: "name"}, {Name: "id"}}, "id"},
		{"no pk falls back to first column", "", false, []Column{{Name: "name"}, {Name: "note"}}, "name"},
		{"no pk, no columns", "", false, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := orderColumnFor(tc.pk, tc.hasPK, tc.cols); got != tc.want {
				t.Fatalf("orderColumnFor(%q, %v, %v) = %q, want %q", tc.pk, tc.hasPK, tc.cols, got, tc.want)
			}
		})
	}
}

// Test: Register makes a driver's schema and rows reachable by name, and an
// unregistered name errors cleanly.
func TestRegisterSchemaAndRows(t *testing.T) {
	fd := newFakeDriver("users", "orders")
	fd.rows["users"] = []map[string]any{{"id": int64(1), "name": "alice"}}

	insp := New()
	insp.Register("primary", fd)

	tables, err := insp.Schema(context.Background(), "primary")
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if len(tables) != 2 {
		t.Fatalf("Schema returned %d tables, want 2", len(tables))
	}

	rows, err := insp.Rows(context.Background(), "primary", "users", 10, 0)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 1 || rows[0]["name"] != "alice" {
		t.Fatalf("Rows = %+v, want one row named alice", rows)
	}

	if _, err := insp.Schema(context.Background(), "nope"); err == nil {
		t.Fatal("Schema on unregistered db: want error, got nil")
	}
	if _, err := insp.Rows(context.Background(), "nope", "users", 10, 0); err == nil {
		t.Fatal("Rows on unregistered db: want error, got nil")
	}

	if !insp.Has("primary") {
		t.Error(`Has("primary") = false, want true`)
	}
	if insp.Has("nope") {
		t.Error(`Has("nope") = true, want false`)
	}
}

// Test: Watch's poller emits a ChangeEvent when a table's fingerprint
// changes between ticks, and does NOT emit while the fingerprint stays the
// same across many subsequent ticks (dedup — no repeat events for an
// unchanged fingerprint).
func TestWatchEmitsOnFingerprintChangeAndDedups(t *testing.T) {
	fd := newFakeDriver("users")
	insp := New()
	insp.Register("primary", fd)

	events, stop := insp.Watch(5 * time.Millisecond)
	defer stop()

	// First tick(s) just establish the baseline fingerprint; nothing should
	// fire yet since nothing changed.
	select {
	case ev := <-events:
		t.Fatalf("unexpected event before any change: %+v", ev)
	case <-time.After(30 * time.Millisecond):
	}

	fd.setFingerprint("users", "v1")

	var ev ChangeEvent
	select {
	case ev = <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for change event")
	}
	if ev.DB != "primary" || ev.Table != "users" {
		t.Fatalf("event = %+v, want DB=primary Table=users", ev)
	}
	if ev.At.IsZero() {
		t.Fatal("event.At is zero")
	}

	// Fingerprint stays at v1 for many further ticks: no further events
	// should be delivered.
	select {
	case ev := <-events:
		t.Fatalf("unexpected duplicate event for unchanged fingerprint: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// Test: stop() halts the poller — no events arrive afterward even if the
// fingerprint keeps changing — and it's safe to call more than once and
// safe to call even if no event was ever consumed.
func TestWatchStop(t *testing.T) {
	fd := newFakeDriver("users")
	insp := New()
	insp.Register("primary", fd)

	events, stop := insp.Watch(5 * time.Millisecond)
	stop()
	stop() // must not panic

	fd.setFingerprint("users", "v1")

	select {
	case ev, ok := <-events:
		if ok {
			t.Fatalf("unexpected event after stop: %+v", ev)
		}
		// channel closed: fine.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel neither closed nor delivered after stop")
	}
}

// Test: multiple registered databases/tables each get independent change
// tracking.
func TestWatchTracksMultipleDatabasesIndependently(t *testing.T) {
	fdA := newFakeDriver("t1")
	fdB := newFakeDriver("t1") // same table name, different db
	insp := New()
	insp.Register("dbA", fdA)
	insp.Register("dbB", fdB)

	events, stop := insp.Watch(5 * time.Millisecond)
	defer stop()

	time.Sleep(20 * time.Millisecond) // let a baseline tick land

	fdA.setFingerprint("t1", "changed")

	select {
	case ev := <-events:
		if ev.DB != "dbA" || ev.Table != "t1" {
			t.Fatalf("event = %+v, want DB=dbA Table=t1", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dbA event")
	}

	// dbB's fingerprint never changed, so it must not have fired.
	select {
	case ev := <-events:
		t.Fatalf("unexpected event from unchanged dbB: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}
