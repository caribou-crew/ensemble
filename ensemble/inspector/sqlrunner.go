package inspector

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// SQLRunner implements orchestrator.SQLRunner (structurally — this package
// doesn't import orchestrator, avoiding a dependency the other direction
// isn't needed for): it executes a seed SQL file against one of the
// databases declared in ensemble.yaml, opening a connection per call using
// the same DSN-building logic (PostgresDSN/MySQLDSN) the inspection
// drivers use.
type SQLRunner struct {
	databases map[string]config.Database
}

// NewSQLRunner returns a SQLRunner that resolves RunFile's dbName against
// databases (typically config.Config.Databases).
func NewSQLRunner(databases map[string]config.Database) *SQLRunner {
	return &SQLRunner{databases: databases}
}

// RunFile reads path and executes it as a `;`-separated sequence of SQL
// statements against dbName, in order, stopping at the first failing
// statement. dbName must be a postgres or mysql database (the only types
// with a SQL execution path); anything else — including an unknown name —
// is an error.
func (r *SQLRunner) RunFile(ctx context.Context, dbName, path string) error {
	db, ok := r.databases[dbName]
	if !ok {
		return fmt.Errorf("inspector: sqlrunner: database %q not configured", dbName)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("inspector: sqlrunner: read %s: %w", path, err)
	}

	conn, err := openSQL(db)
	if err != nil {
		return fmt.Errorf("inspector: sqlrunner: %s: %w", dbName, err)
	}
	defer conn.Close()

	for n, stmt := range splitSQLStatements(string(data)) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("inspector: sqlrunner: %s: statement %d: %w", dbName, n+1, err)
		}
	}
	return nil
}

// openSQL opens a database/sql connection for db, dispatching on its
// configured Type.
func openSQL(db config.Database) (*sql.DB, error) {
	switch db.Type {
	case "postgres":
		return sql.Open("pgx", PostgresDSN(db))
	case "mysql":
		return sql.Open("mysql", MySQLDSN(db))
	default:
		return nil, fmt.Errorf("no SQL execution path for database type %q", db.Type)
	}
}

// splitSQLStatements splits a SQL script into individual statements on
// unquoted `;`, honoring single- and double-quoted sections (including
// their escape-by-doubling convention for an embedded quote, shared by
// postgres and mysql) and `--` line comments, so a semicolon inside a
// string literal, quoted identifier, or comment does not end a statement
// early. The final statement need not be `;`-terminated. Empty/
// whitespace-only statements (e.g. from a trailing `;` or blank input) are
// omitted.
func splitSQLStatements(script string) []string {
	var stmts []string
	var cur strings.Builder

	runes := []rune(script)
	inSingle := false
	inDouble := false
	inLineComment := false

	for i := 0; i < len(runes); i++ {
		c := runes[i]

		if inLineComment {
			cur.WriteRune(c)
			if c == '\n' {
				inLineComment = false
			}
			continue
		}
		if inSingle {
			cur.WriteRune(c)
			if c == '\'' {
				if i+1 < len(runes) && runes[i+1] == '\'' {
					cur.WriteRune(runes[i+1])
					i++
				} else {
					inSingle = false
				}
			}
			continue
		}
		if inDouble {
			cur.WriteRune(c)
			if c == '"' {
				if i+1 < len(runes) && runes[i+1] == '"' {
					cur.WriteRune(runes[i+1])
					i++
				} else {
					inDouble = false
				}
			}
			continue
		}

		switch {
		case c == '\'':
			inSingle = true
			cur.WriteRune(c)
		case c == '"':
			inDouble = true
			cur.WriteRune(c)
		case c == '-' && i+1 < len(runes) && runes[i+1] == '-':
			inLineComment = true
			cur.WriteRune(c)
		case c == ';':
			stmts = append(stmts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(c)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		stmts = append(stmts, cur.String())
	}

	out := stmts[:0]
	for _, s := range stmts {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
