package inspector

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// MySQLDSN builds a mysql connection string for db, following the official
// mysql image's env var conventions: MYSQL_USER/MYSQL_PASSWORD when set
// (an app user), else root using MYSQL_ROOT_PASSWORD; MYSQL_DATABASE names
// the database (empty connects with no default database selected).
// ensemble always publishes a database's container port to the same host
// port (see orchestrator's dockerRunDatabase), so the driver always dials
// 127.0.0.1:db.Port.
func MySQLDSN(db config.Database) string {
	user := db.Env["MYSQL_USER"]
	password := db.Env["MYSQL_PASSWORD"]
	if user == "" {
		user = "root"
		password = db.Env["MYSQL_ROOT_PASSWORD"]
	}
	name := db.Env["MYSQL_DATABASE"]

	// Built via mysql.Config/FormatDSN rather than string concatenation so
	// user/password values containing DSN-special characters (":", "@",
	// "/") round-trip correctly — the DSN format isn't a URL, and
	// go-sql-driver's own parser expects its own escaping rules.
	cfg := mysql.NewConfig()
	cfg.User = user
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("127.0.0.1:%d", db.Port)
	cfg.DBName = name
	return cfg.FormatDSN()
}

// MySQLDriver is the Driver implementation backed by a live mysql
// connection (via go-sql-driver/mysql).
type MySQLDriver struct {
	db *sql.DB
}

// NewMySQLDriver opens (lazily) a mysql connection over dsn (a
// go-sql-driver/mysql DSN — see MySQLDSN).
func NewMySQLDriver(dsn string) (*MySQLDriver, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("inspector: mysql: open: %w", err)
	}
	return &MySQLDriver{db: db}, nil
}

// Close releases the underlying connection pool.
func (m *MySQLDriver) Close() error {
	return m.db.Close()
}

// Tables lists every base table in the connection's current database, with
// columns.
func (m *MySQLDriver) Tables(ctx context.Context) ([]Table, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'
		ORDER BY table_name`)
	if err != nil {
		return nil, fmt.Errorf("inspector: mysql: list tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("inspector: mysql: scan table name: %w", err)
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspector: mysql: list tables: %w", err)
	}

	tables := make([]Table, 0, len(names))
	for _, n := range names {
		cols, err := m.columns(ctx, n)
		if err != nil {
			return nil, err
		}
		tables = append(tables, Table{Name: n, Columns: cols})
	}
	return tables, nil
}

func (m *MySQLDriver) columns(ctx context.Context, table string) ([]Column, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT column_name, data_type, is_nullable = 'YES'
		FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ?
		ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("inspector: mysql: columns %s: %w", table, err)
	}
	defer rows.Close()

	var cols []Column
	for rows.Next() {
		var c Column
		var nullable int64 // MySQL has no native bool; compares to 0/1
		if err := rows.Scan(&c.Name, &c.Type, &nullable); err != nil {
			return nil, fmt.Errorf("inspector: mysql: scan column: %w", err)
		}
		c.Nullable = nullable != 0
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// Rows returns up to limit rows of table, skipping offset.
func (m *MySQLDriver) Rows(ctx context.Context, table string, limit, offset int) ([]map[string]any, error) {
	query := fmt.Sprintf("SELECT * FROM %s LIMIT ? OFFSET ?", quoteIdentMySQL(table))
	rows, err := m.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("inspector: mysql: rows %s: %w", table, err)
	}
	return scanRowsToMaps(rows)
}

// Fingerprint is count(*) plus max(pk) when table has a single-column
// primary key, else count(*) alone.
func (m *MySQLDriver) Fingerprint(ctx context.Context, table string) (string, error) {
	pk, ok, err := m.primaryKeyColumn(ctx, table)
	if err != nil {
		return "", err
	}
	if !ok {
		var count int64
		q := fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteIdentMySQL(table))
		if err := m.db.QueryRowContext(ctx, q).Scan(&count); err != nil {
			return "", fmt.Errorf("inspector: mysql: fingerprint %s: %w", table, err)
		}
		return fmt.Sprintf("count=%d", count), nil
	}

	var count int64
	var max sql.NullString
	q := fmt.Sprintf("SELECT COUNT(*), CAST(MAX(%s) AS CHAR) FROM %s", quoteIdentMySQL(pk), quoteIdentMySQL(table))
	if err := m.db.QueryRowContext(ctx, q).Scan(&count, &max); err != nil {
		return "", fmt.Errorf("inspector: mysql: fingerprint %s: %w", table, err)
	}
	return fmt.Sprintf("count=%d;max=%s", count, max.String), nil
}

// primaryKeyColumn returns table's primary key column, if it has exactly
// one (multi-column keys fall back to count(*)-only fingerprinting).
func (m *MySQLDriver) primaryKeyColumn(ctx context.Context, table string) (string, bool, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT column_name
		FROM information_schema.key_column_usage
		WHERE table_schema = DATABASE() AND table_name = ? AND constraint_name = 'PRIMARY'
		ORDER BY ordinal_position`, table)
	if err != nil {
		return "", false, fmt.Errorf("inspector: mysql: pk %s: %w", table, err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return "", false, fmt.Errorf("inspector: mysql: scan pk %s: %w", table, err)
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("inspector: mysql: pk %s: %w", table, err)
	}
	if len(cols) != 1 {
		return "", false, nil
	}
	return cols[0], true, nil
}

// quoteIdentMySQL backtick-quotes a mysql identifier, doubling any embedded
// backtick.
func quoteIdentMySQL(ident string) string {
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}
