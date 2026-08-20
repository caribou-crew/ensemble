package inspector

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// pgDatabaseFromDSN parses a "postgres://user:pass@host:port/db?..." DSN
// (the form ENSEMBLE_TEST_PG_DSN uses) back into a config.Database, so the
// integration test exercises SQLRunner's own PostgresDSN-building path
// rather than bypassing it with a pre-built DSN.
func pgDatabaseFromDSN(t *testing.T, dsn string) config.Database {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse %q: %v", dsn, err)
	}
	portStr := u.Port()
	if portStr == "" {
		t.Fatalf("DSN %q has no port", dsn)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("DSN %q: bad port: %v", dsn, err)
	}
	user := u.User.Username()
	password, _ := u.User.Password()
	name := strings.TrimPrefix(u.Path, "/")

	return config.Database{
		Type: "postgres",
		Port: port,
		Env: map[string]string{
			"POSTGRES_USER":     user,
			"POSTGRES_PASSWORD": password,
			"POSTGRES_DB":       name,
		},
	}
}

// mysqlDatabaseFromDSN parses a go-sql-driver/mysql DSN (the form
// ENSEMBLE_TEST_MYSQL_DSN uses) back into a config.Database, exercising
// SQLRunner's own MySQLDSN-building path.
func mysqlDatabaseFromDSN(t *testing.T, dsn string) config.Database {
	t.Helper()
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse %q: %v", dsn, err)
	}
	host, portStr, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		t.Fatalf("DSN %q: bad addr %q: %v", dsn, cfg.Addr, err)
	}
	_ = host
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("DSN %q: bad port: %v", dsn, err)
	}

	env := map[string]string{"MYSQL_DATABASE": cfg.DBName}
	if cfg.User == "root" {
		env["MYSQL_ROOT_PASSWORD"] = cfg.Passwd
	} else {
		env["MYSQL_USER"] = cfg.User
		env["MYSQL_PASSWORD"] = cfg.Passwd
	}

	return config.Database{
		Type: "mysql",
		Port: port,
		Env:  env,
	}
}

// Test: splitSQLStatements splits on unquoted semicolons only — a
// semicolon inside a single-quoted string literal or a double-quoted
// identifier must not end a statement early, and doubled quotes (the
// escape-by-doubling convention both postgres and mysql use) must not be
// mistaken for the closing quote.
func TestSplitSQLStatements(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   []string
	}{
		{
			name:   "simple two statements",
			script: "INSERT INTO t (a) VALUES (1); INSERT INTO t (a) VALUES (2);",
			want: []string{
				"INSERT INTO t (a) VALUES (1)",
				"INSERT INTO t (a) VALUES (2)",
			},
		},
		{
			name:   "no trailing semicolon",
			script: "SELECT 1; SELECT 2",
			want:   []string{"SELECT 1", "SELECT 2"},
		},
		{
			name:   "semicolon inside single-quoted string literal",
			script: "INSERT INTO t (a) VALUES ('a;b'); INSERT INTO t (a) VALUES ('c');",
			want: []string{
				"INSERT INTO t (a) VALUES ('a;b')",
				"INSERT INTO t (a) VALUES ('c')",
			},
		},
		{
			name:   "doubled single-quote escape does not end the string early",
			script: "INSERT INTO t (a) VALUES ('it''s; still one'); SELECT 1;",
			want: []string{
				"INSERT INTO t (a) VALUES ('it''s; still one')",
				"SELECT 1",
			},
		},
		{
			name:   "semicolon inside double-quoted identifier",
			script: `CREATE TABLE "weird;name" (id int); SELECT 1;`,
			want: []string{
				`CREATE TABLE "weird;name" (id int)`,
				"SELECT 1",
			},
		},
		{
			name:   "blank and whitespace-only statements omitted",
			script: "  ;\nSELECT 1;\n  \n;  ",
			want:   []string{"SELECT 1"},
		},
		{
			name:   "empty script",
			script: "",
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitSQLStatements(tc.script)
			var trimmed []string
			for _, s := range got {
				trimmed = append(trimmed, trimAll(s))
			}
			if !reflect.DeepEqual(trimmed, trimAllSlice(tc.want)) {
				t.Fatalf("splitSQLStatements(%q) = %#v, want %#v", tc.script, trimmed, tc.want)
			}
		})
	}
}

// trimAll/trimAllSlice: the test cases above assert on statement content
// after the same TrimSpace RunFile applies before executing, since
// splitSQLStatements preserves surrounding whitespace by design (it's a
// pure split, not a trim).
func trimAll(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\n' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func trimAllSlice(ss []string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = trimAll(s)
	}
	return out
}

// Test: RunFile errors cleanly for a database name not present in the
// configured map, without touching the filesystem.
func TestSQLRunnerRunFileUnknownDatabase(t *testing.T) {
	r := NewSQLRunner(map[string]config.Database{})
	err := r.RunFile(context.Background(), "nope", "/does/not/matter.sql")
	if err == nil {
		t.Fatal("expected an error for an unconfigured database")
	}
}

// Test: RunFile against a live postgres executes every statement in
// order. Skipped unless ENSEMBLE_TEST_PG_DSN is set — reuses the same DSN
// convention as postgres_test.go, but parses it back into a
// config.Database so SQLRunner exercises its own DSN-building path too.
func TestSQLRunnerRunFilePostgres(t *testing.T) {
	dsn := os.Getenv("ENSEMBLE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("ENSEMBLE_TEST_PG_DSN not set; skipping live postgres sqlrunner integration test")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open verification connection: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`DROP TABLE IF EXISTS inspector_sqlrunner_test`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DROP TABLE IF EXISTS inspector_sqlrunner_test`)
	})

	dir := t.TempDir()
	script := "CREATE TABLE inspector_sqlrunner_test (id int, note text);\n" +
		"INSERT INTO inspector_sqlrunner_test (id, note) VALUES (1, 'a;b'''), (2, 'c');\n"
	path := filepath.Join(dir, "seed.sql")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	r := NewSQLRunner(map[string]config.Database{
		"primary": pgDatabaseFromDSN(t, dsn),
	})
	if err := r.RunFile(context.Background(), "primary", path); err != nil {
		t.Fatalf("RunFile: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inspector_sqlrunner_test`).Scan(&count); err != nil {
		t.Fatalf("verify count: %v", err)
	}
	if count != 2 {
		t.Fatalf("row count = %d, want 2", count)
	}
}

// Test: RunFile against a live mysql executes every statement in order.
// Skipped unless ENSEMBLE_TEST_MYSQL_DSN is set.
func TestSQLRunnerRunFileMySQL(t *testing.T) {
	dsn := os.Getenv("ENSEMBLE_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ENSEMBLE_TEST_MYSQL_DSN not set; skipping live mysql sqlrunner integration test")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open verification connection: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`DROP TABLE IF EXISTS inspector_sqlrunner_test`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DROP TABLE IF EXISTS inspector_sqlrunner_test`)
	})

	dir := t.TempDir()
	script := "CREATE TABLE inspector_sqlrunner_test (id INT, note VARCHAR(255));\n" +
		"INSERT INTO inspector_sqlrunner_test (id, note) VALUES (1, 'a;b'''), (2, 'c');\n"
	path := filepath.Join(dir, "seed.sql")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	r := NewSQLRunner(map[string]config.Database{
		"primary": mysqlDatabaseFromDSN(t, dsn),
	})
	if err := r.RunFile(context.Background(), "primary", path); err != nil {
		t.Fatalf("RunFile: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inspector_sqlrunner_test`).Scan(&count); err != nil {
		t.Fatalf("verify count: %v", err)
	}
	if count != 2 {
		t.Fatalf("row count = %d, want 2", count)
	}
}
