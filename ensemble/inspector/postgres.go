package inspector

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// PostgresDSN builds a postgres connection string for db, following the
// official postgres image's env var conventions: POSTGRES_USER (default
// "postgres"), POSTGRES_PASSWORD, POSTGRES_DB (default: same as user).
// ensemble always publishes a database's container port to the same host
// port (see orchestrator's dockerRunDatabase), so the driver always dials
// 127.0.0.1:db.Port.
func PostgresDSN(db config.Database) string {
	user := db.Env["POSTGRES_USER"]
	if user == "" {
		user = "postgres"
	}
	password := db.Env["POSTGRES_PASSWORD"]
	name := db.Env["POSTGRES_DB"]
	if name == "" {
		name = user
	}

	var userinfo *url.Userinfo
	if password != "" {
		userinfo = url.UserPassword(user, password)
	} else {
		userinfo = url.User(user)
	}

	u := url.URL{
		Scheme: "postgres",
		User:   userinfo,
		Host:   fmt.Sprintf("127.0.0.1:%d", db.Port),
		Path:   "/" + name,
	}
	q := url.Values{}
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	return u.String()
}

// PostgresDriver is the Driver implementation backed by a live postgres
// connection (via jackc/pgx's database/sql driver).
type PostgresDriver struct {
	db *sql.DB
}

// NewPostgresDriver opens (lazily — database/sql pools connections and
// dials on first use) a postgres connection over dsn.
func NewPostgresDriver(dsn string) (*PostgresDriver, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("inspector: postgres: open: %w", err)
	}
	return &PostgresDriver{db: db}, nil
}

// Close releases the underlying connection pool.
func (p *PostgresDriver) Close() error {
	return p.db.Close()
}

// Tables lists every base table in the public schema, with columns.
func (p *PostgresDriver) Tables(ctx context.Context) ([]Table, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
		ORDER BY table_name`)
	if err != nil {
		return nil, fmt.Errorf("inspector: postgres: list tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("inspector: postgres: scan table name: %w", err)
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspector: postgres: list tables: %w", err)
	}

	tables := make([]Table, 0, len(names))
	for _, n := range names {
		cols, err := p.columns(ctx, n)
		if err != nil {
			return nil, err
		}
		tables = append(tables, Table{Name: n, Columns: cols})
	}
	return tables, nil
}

func (p *PostgresDriver) columns(ctx context.Context, table string) ([]Column, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT column_name, data_type, is_nullable = 'YES'
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1
		ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("inspector: postgres: columns %s: %w", table, err)
	}
	defer rows.Close()

	var cols []Column
	for rows.Next() {
		var c Column
		if err := rows.Scan(&c.Name, &c.Type, &c.Nullable); err != nil {
			return nil, fmt.Errorf("inspector: postgres: scan column: %w", err)
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// Rows returns up to limit rows of table, skipping offset.
func (p *PostgresDriver) Rows(ctx context.Context, table string, limit, offset int) ([]map[string]any, error) {
	query := fmt.Sprintf("SELECT * FROM %s LIMIT $1 OFFSET $2", quoteIdentPG(table))
	rows, err := p.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("inspector: postgres: rows %s: %w", table, err)
	}
	return scanRowsToMaps(rows)
}

// Fingerprint is count(*) plus max(pk) when table has a single-column
// primary key, else count(*) alone.
func (p *PostgresDriver) Fingerprint(ctx context.Context, table string) (string, error) {
	pk, ok, err := p.primaryKeyColumn(ctx, table)
	if err != nil {
		return "", err
	}
	if !ok {
		var count int64
		q := fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteIdentPG(table))
		if err := p.db.QueryRowContext(ctx, q).Scan(&count); err != nil {
			return "", fmt.Errorf("inspector: postgres: fingerprint %s: %w", table, err)
		}
		return fmt.Sprintf("count=%d", count), nil
	}

	var count int64
	var max sql.NullString
	q := fmt.Sprintf("SELECT COUNT(*), MAX(%s)::text FROM %s", quoteIdentPG(pk), quoteIdentPG(table))
	if err := p.db.QueryRowContext(ctx, q).Scan(&count, &max); err != nil {
		return "", fmt.Errorf("inspector: postgres: fingerprint %s: %w", table, err)
	}
	return fmt.Sprintf("count=%d;max=%s", count, max.String), nil
}

// primaryKeyColumn returns table's primary key column, if it has exactly
// one (multi-column keys fall back to count(*)-only fingerprinting — the
// max() shortcut doesn't generalize to composite keys).
func (p *PostgresDriver) primaryKeyColumn(ctx context.Context, table string) (string, bool, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT kcu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
		WHERE tc.constraint_type = 'PRIMARY KEY' AND tc.table_schema = 'public' AND tc.table_name = $1
		ORDER BY kcu.ordinal_position`, table)
	if err != nil {
		return "", false, fmt.Errorf("inspector: postgres: pk %s: %w", table, err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return "", false, fmt.Errorf("inspector: postgres: scan pk %s: %w", table, err)
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("inspector: postgres: pk %s: %w", table, err)
	}
	if len(cols) != 1 {
		return "", false, nil
	}
	return cols[0], true, nil
}

// quoteIdentPG double-quotes a postgres identifier, doubling any embedded
// quote — table names come from information_schema (trusted), but this
// keeps Rows/Fingerprint correct for identifiers containing special
// characters (mixed case, spaces) that postgres itself allows when quoted.
func quoteIdentPG(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}
