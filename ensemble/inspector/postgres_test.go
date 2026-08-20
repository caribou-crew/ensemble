package inspector

import (
	"context"
	"os"
	"testing"

	"github.com/caribou-crew/ensemble/ensemble/config"
)

// Test: PostgresDSN applies the official postgres image's env var
// conventions and defaults — unit-testable without a live database.
func TestPostgresDSN(t *testing.T) {
	cases := []struct {
		name string
		db   config.Database
		want string
	}{
		{
			name: "defaults",
			db:   config.Database{Port: 5432},
			want: "postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable",
		},
		{
			name: "explicit user db password",
			db: config.Database{
				Port: 5433,
				Env: map[string]string{
					"POSTGRES_USER":     "app",
					"POSTGRES_PASSWORD": "secret",
					"POSTGRES_DB":       "appdb",
				},
			},
			want: "postgres://app:secret@127.0.0.1:5433/appdb?sslmode=disable",
		},
		{
			name: "user set, db defaults to user",
			db: config.Database{
				Port: 5434,
				Env:  map[string]string{"POSTGRES_USER": "app"},
			},
			want: "postgres://app@127.0.0.1:5434/app?sslmode=disable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PostgresDSN(tc.db)
			if got != tc.want {
				t.Fatalf("PostgresDSN(%+v) = %q, want %q", tc.db, got, tc.want)
			}
		})
	}
}

// Test: against a live postgres, Tables/Rows/Fingerprint see real schema
// and data and Fingerprint changes when a row is inserted. Skipped unless
// ENSEMBLE_TEST_PG_DSN is set (e.g. to
// "postgres://postgres:postgres@127.0.0.1:15432/postgres?sslmode=disable"
// pointed at a throwaway postgres container).
func TestPostgresDriverIntegration(t *testing.T) {
	dsn := os.Getenv("ENSEMBLE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("ENSEMBLE_TEST_PG_DSN not set; skipping live postgres integration test")
	}

	drv, err := NewPostgresDriver(dsn)
	if err != nil {
		t.Fatalf("NewPostgresDriver: %v", err)
	}
	t.Cleanup(func() { drv.Close() })

	ctx := context.Background()
	if _, err := drv.db.ExecContext(ctx, `DROP TABLE IF EXISTS inspector_test_widgets`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := drv.db.ExecContext(ctx, `
		CREATE TABLE inspector_test_widgets (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			note TEXT
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

	// Final-review finding #16 (promoted from task 2.5): Rows must ORDER BY
	// so LIMIT/OFFSET paging is deterministic — page through one row at a
	// time and confirm it's the same order (by primary key "id") as a
	// single unpaged fetch, with no duplicate or skipped id.
	page1, err := drv.Rows(ctx, "inspector_test_widgets", 1, 0)
	if err != nil {
		t.Fatalf("Rows page1: %v", err)
	}
	page2, err := drv.Rows(ctx, "inspector_test_widgets", 1, 1)
	if err != nil {
		t.Fatalf("Rows page2: %v", err)
	}
	if len(page1) != 1 || len(page2) != 1 {
		t.Fatalf("paged rows = %+v / %+v, want 1 row each", page1, page2)
	}
	if page1[0]["id"] != rows[0]["id"] || page2[0]["id"] != rows[1]["id"] {
		t.Fatalf("paged order (id=%v, id=%v) does not match unpaged order (id=%v, id=%v) — Rows is not deterministically ordered",
			page1[0]["id"], page2[0]["id"], rows[0]["id"], rows[1]["id"])
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
