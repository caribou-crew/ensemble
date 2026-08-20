package capture

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caribou-crew/ensemble/retrace/runs"
)

func TestMarkerDoorAppendsStartAndEndRecords(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(NewMarkerDoor(runs.Paths{RunDir: dir}, func() time.Time { return now }))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/group", "application/json", strings.NewReader(`{"name":"checkout"}`))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start marker: %v status=%v", err, resp.StatusCode)
	}
	resp, _ = http.Post(srv.URL+"/group/end", "application/json", strings.NewReader(`{}`))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("end marker status = %d", resp.StatusCode)
	}
	recs, _ := runs.ReadGroupRecords(runs.Paths{RunDir: dir})
	if len(recs) != 2 || recs[0].Name != "checkout" || recs[1].Phase != "end" {
		t.Fatalf("records = %+v", recs)
	}
}

// 400 is the healthy answer here and is what a preflight probe keys on: the
// door exists and refused a nameless marker. Anything else means some OTHER
// server holds the port.
func TestMarkerDoorRejectsAnUnnamedStart(t *testing.T) {
	srv := httptest.NewServer(NewMarkerDoor(runs.Paths{RunDir: t.TempDir()}, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/group", "application/json", strings.NewReader(`{"name":"  "}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// A malformed body must not read as an empty one — otherwise an adapter
// posting garbage looks exactly like an adapter posting nothing.
func TestMarkerDoorRejectsAMalformedBody(t *testing.T) {
	srv := httptest.NewServer(NewMarkerDoor(runs.Paths{RunDir: t.TempDir()}, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/group", "application/json", strings.NewReader(`{"name":`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// The door is loopback-bound, but a page the developer has open can still
// reach 127.0.0.1. This asserts the door is behind httpguard.Handler and
// not a hand-rolled subset of it: cross-site is refused, and so is a Host
// header naming somebody else's domain (the DNS-rebinding case the old
// inlined copy did not cover).
func TestMarkerDoorRejectsCrossSiteAndReboundHosts(t *testing.T) {
	dir := t.TempDir()
	h := NewMarkerDoor(runs.Paths{RunDir: dir}, nil)

	crossSite := httptest.NewRequest("POST", "http://127.0.0.1/group", strings.NewReader(`{"name":"checkout"}`))
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, crossSite)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site status = %d, want 403", rec.Code)
	}

	rebound := httptest.NewRequest("POST", "http://127.0.0.1/group", strings.NewReader(`{"name":"checkout"}`))
	rebound.Host = "attacker.example"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, rebound)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("rebound-Host status = %d, want 403", rec.Code)
	}

	if recs, _ := runs.ReadGroupRecords(runs.Paths{RunDir: dir}); len(recs) != 0 {
		t.Fatalf("a rejected request wrote %d marker records", len(recs))
	}
}
