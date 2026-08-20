package inspector

import (
	"context"
	"os"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// Test: MySQLDSN applies the official mysql image's env var conventions —
// unit-testable without a live database.
func TestMySQLDSN(t *testing.T) {
	cases := []struct {
		name string
		db   config.Database
		want string
	}{
		{
			name: "root default",
			db:   config.Database{Port: 3306, Env: map[string]string{"MYSQL_ROOT_PASSWORD": "secret"}},
			want: "root:secret@tcp(127.0.0.1:3306)/",
		},
		{
			name: "app user",
			db: config.Database{
				Port: 3307,
				Env: map[string]string{
					"MYSQL_ROOT_PASSWORD": "rootsecret",
					"MYSQL_USER":          "app",
					"MYSQL_PASSWORD":      "appsecret",
					"MYSQL_DATABASE":      "appdb",
				},
			},
			want: "app:appsecret@tcp(127.0.0.1:3307)/appdb",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MySQLDSN(tc.db)
			if got != tc.want {
				t.Fatalf("MySQLDSN(%+v) = %q, want %q", tc.db, got, tc.want)
			}
		})
	}
}

// Test: against a live mysql, Tables/Rows/Fingerprint see real schema and
// data and Fingerprint changes when a row is inserted. Skipped unless
// ENSEMBLE_TEST_MYSQL_DSN is set (e.g. to
// "root:mysql@tcp(127.0.0.1:13306)/testdb" pointed at a throwaway mysql
// container).
func TestMySQLDriverIntegration(t *testing.T) {
	dsn := os.Getenv("ENSEMBLE_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ENSEMBLE_TEST_MYSQL_DSN not set; skipping live mysql integration test")
	}

	drv, err := NewMySQLDriver(dsn)
	if err != nil {
		t.Fatalf("NewMySQLDriver: %v", err)
	}
	t.Cleanup(func() { drv.Close() })

	ctx := context.Background()
	if _, err := drv.db.ExecContext(ctx, `DROP TABLE IF EXISTS inspector_test_widgets`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := drv.db.ExecContext(ctx, `
		CREATE TABLE inspector_test_widgets (
			id INT AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			note VARCHAR(255)
		)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		drv.db.ExecContext(context.Background(), `DROP TABLE IF EXISTS inspector_test_widgets`)
	})
	if _, err := drv.db.ExecContext(ctx, `INSERT INTO inspector_test_widgets (name) VALUES ('a'), ('b')`); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	tables, err := drv.Tables(ctx)
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	var widgets *Table
	for i := range tables {
		if tables[i].Name == "inspector_test_widgets" {
			widgets = &tables[i]
		}
	}
	if widgets == nil {
		t.Fatalf("Tables did not include inspector_test_widgets: %+v", tables)
	}
	wantCols := map[string]bool{"id": false, "name": false, "note": true}
	if len(widgets.Columns) != len(wantCols) {
		t.Fatalf("columns = %+v, want %d columns", widgets.Columns, len(wantCols))
	}
	for _, c := range widgets.Columns {
		wantNullable, ok := wantCols[c.Name]
		if !ok {
			t.Fatalf("unexpected column %q", c.Name)
		}
		if c.Nullable != wantNullable {
			t.Fatalf("column %q nullable = %v, want %v", c.Name, c.Nullable, wantNullable)
		}
	}

	rows, err := drv.Rows(ctx, "inspector_test_widgets", 10, 0)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Rows = %d, want 2: %+v", len(rows), rows)
	}

	fp1, err := drv.Fingerprint(ctx, "inspector_test_widgets")
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}

	if _, err := drv.db.ExecContext(ctx, `INSERT INTO inspector_test_widgets (name) VALUES ('c')`); err != nil {
		t.Fatalf("insert third row: %v", err)
	}
	fp2, err := drv.Fingerprint(ctx, "inspector_test_widgets")
	if err != nil {
		t.Fatalf("Fingerprint after insert: %v", err)
	}
	if fp1 == fp2 {
		t.Fatalf("Fingerprint did not change after insert: both %q", fp1)
	}
}
