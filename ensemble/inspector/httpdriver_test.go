package inspector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Test: Tables/Rows/Fingerprint happy path against a fake service
// implementing the three-route contract, including that headers are sent
// on every request.
func TestHTTPDriverHappyPath(t *testing.T) {
	var gotAuth []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/inspect/tables":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tables": []Table{
					{Name: "cards", Columns: []Column{{Name: "token", Type: "string", Nullable: false}}},
				},
			})
		case "/inspect/rows":
			if r.URL.Query().Get("table") != "cards" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"rows": []map[string]any{{"token": "abc"}},
			})
		case "/inspect/fingerprint":
			if r.URL.Query().Get("table") != "cards" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"fingerprint": "count=1"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	d := NewHTTPDriver(ts.URL+"/inspect", map[string]string{"Authorization": "Basic xyz"})

	tables, err := d.Tables(context.Background())
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	if len(tables) != 1 || tables[0].Name != "cards" {
		t.Fatalf("Tables = %#v, want one table named cards", tables)
	}

	rows, err := d.Rows(context.Background(), "cards", 50, 0)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 1 || rows[0]["token"] != "abc" {
		t.Fatalf("Rows = %#v, want one row with token abc", rows)
	}

	fp, err := d.Fingerprint(context.Background(), "cards")
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if fp != "count=1" {
		t.Fatalf("Fingerprint = %q, want count=1", fp)
	}

	for _, got := range gotAuth {
		if got != "Basic xyz" {
			t.Errorf("Authorization header = %q, want Basic xyz on every request", got)
		}
	}
}

// Test: Rows/Fingerprint against an unknown table return a "not found"
// error, mirroring Inspector.Rows/Schema's error for an unregistered
// database.
func TestHTTPDriverUnknownTable404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)

	d := NewHTTPDriver(ts.URL, nil)

	if _, err := d.Rows(context.Background(), "ghost", 10, 0); err == nil {
		t.Fatal("Rows: expected error for unknown table, got nil")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Rows error = %v, want it to mention not found", err)
	}

	if _, err := d.Fingerprint(context.Background(), "ghost"); err == nil {
		t.Fatal("Fingerprint: expected error for unknown table, got nil")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Fingerprint error = %v, want it to mention not found", err)
	}
}

// Test: a non-2xx, non-404 response surfaces as a plain error.
func TestHTTPDriverNon2xxIsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(ts.Close)

	d := NewHTTPDriver(ts.URL, nil)

	_, err := d.Tables(context.Background())
	if err == nil {
		t.Fatal("Tables: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("Tables error = %v, want it to mention status 500", err)
	}
}

// Test: an already-cancelled context propagates as an error instead of
// issuing the request.
func TestHTTPDriverContextCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tables": []Table{}})
	}))
	t.Cleanup(ts.Close)

	d := NewHTTPDriver(ts.URL, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := d.Tables(ctx); err == nil {
		t.Fatal("Tables: expected error from cancelled context, got nil")
	}
}

// Test: a slow backend that exceeds the caller's deadline surfaces as an
// error rather than hanging.
func TestHTTPDriverContextDeadlineExceeded(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"tables": []Table{}})
	}))
	t.Cleanup(ts.Close)

	d := NewHTTPDriver(ts.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	if _, err := d.Tables(ctx); err == nil {
		t.Fatal("Tables: expected error from deadline exceeded, got nil")
	}
}
