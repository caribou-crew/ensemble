package capture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
	recs, _, _ := runs.ReadGroupRecords(runs.Paths{RunDir: dir})
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

	if recs, _, _ := runs.ReadGroupRecords(runs.Paths{RunDir: dir}); len(recs) != 0 {
		t.Fatalf("a rejected request wrote %d marker records", len(recs))
	}
}

// TestOnAdmittedCountsWhatTheGuardAdmittedNotWhatTheRouterAccepted turns
// NewMarkerDoorCounted's doc comment into something a mutation can kill.
//
// The comment used to assert the opposite of the code for half its cases —
// it named "a nameless-marker 400" among the requests the hook never fires
// for, when the hook is installed OUTSIDE the mux and therefore fires
// before dispatch. capture.Assess's doc described the same mechanism
// correctly ("the mux counts the plan's own preflight probe, and a
// 405/404/malformed-body 400") and neutralises the inflation by
// construction. The code was right and the comment was wrong; nothing
// pinned either reading, which is how they came to disagree.
//
// Both halves are asserted from one table, because the boundary is the
// whole point: the GUARD's rejections are uncounted, the MUX's are not.
func TestOnAdmittedCountsWhatTheGuardAdmittedNotWhatTheRouterAccepted(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path, body string
		crossSite  bool
		wantStatus int
		wantFired  int
	}{
		{"a good marker", "/group", `{"name":"checkout"}`, false, http.StatusNoContent, 1},
		// Admitted by the guard, refused by the router. Counted — this is
		// the row the old comment denied.
		{"a nameless marker", "/group", `{"name":""}`, false, http.StatusBadRequest, 1},
		{"a malformed body", "/group", `{`, false, http.StatusBadRequest, 1},
		{"a stray port probe", "/nope", `{}`, false, http.StatusNotFound, 1},
		// Refused by the guard, never reaches the router. Uncounted — and
		// this is the half the placement exists for: Session.RequestsSeen()
		// == 0 is how Task 6 says "the app never routed through us", so a
		// cross-site POST must not be able to disarm it.
		{"a cross-site POST", "/group", `{"name":"checkout"}`, true, http.StatusForbidden, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fired := 0
			srv := httptest.NewServer(NewMarkerDoorCounted(runs.Paths{RunDir: t.TempDir()}, nil, func() { fired++ }))
			defer srv.Close()

			req, err := http.NewRequest(http.MethodPost, srv.URL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			if tc.crossSite {
				req.Header.Set("Origin", "http://evil.example")
				req.Header.Set("Sec-Fetch-Site", "cross-site")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if fired != tc.wantFired {
				t.Fatalf("onAdmitted fired %d time(s), want %d — the hook counts what the GUARD admitted, not what the ROUTER accepted", fired, tc.wantFired)
			}
		})
	}
}

// TestIdentifyNamesTheOwningRun covers the endpoint's whole reason to
// exist: turning a bare port into "which retrace run holds this".
func TestIdentifyNamesTheOwningRun(t *testing.T) {
	dir := t.TempDir()
	p := runs.Paths{RunDir: dir}
	started := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	if err := runs.MarkRunning(p, runs.Running{
		App: "shop", Flow: "checkout", RunID: "20260826T090000Z-abc1234",
		ProxyURL: "http://127.0.0.1:53221", StartedAt: started,
	}); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	srv := httptest.NewServer(NewMarkerDoor(p, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/identify")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["tool"] != "retrace" {
		t.Errorf("tool = %v, want retrace", got["tool"])
	}
	if got["ownerRecorded"] != true {
		t.Errorf("ownerRecorded = %v, want true", got["ownerRecorded"])
	}
	// The pid must be THIS process — MarkRunning stamps it, and a door that
	// echoed a caller-supplied pid would name a process that never existed.
	if pid, _ := got["pid"].(float64); int(pid) != os.Getpid() {
		t.Errorf("pid = %v, want %d", got["pid"], os.Getpid())
	}
	for k, want := range map[string]string{
		"app": "shop", "flow": "checkout",
		"runId": "20260826T090000Z-abc1234", "proxyUrl": "http://127.0.0.1:53221",
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %q", k, got[k], want)
		}
	}
}

// TestIdentifyWithNoOwnerRecordStillProvesItIsRetrace: the replay server
// opens a marker door and writes no owner record. Answering at all is the
// first half of the question ("is this retrace?") and must not depend on
// the second half being answerable.
func TestIdentifyWithNoOwnerRecordStillProvesItIsRetrace(t *testing.T) {
	srv := httptest.NewServer(NewMarkerDoor(runs.Paths{RunDir: t.TempDir()}, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/identify")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a door with no owner record is still a retrace door", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["tool"] != "retrace" {
		t.Errorf("tool = %v, want retrace", got["tool"])
	}
	if got["ownerRecorded"] != false {
		t.Errorf("ownerRecorded = %v, want false", got["ownerRecorded"])
	}
	if _, present := got["pid"]; present {
		t.Error("a door with no owner record reported a pid")
	}
}

// TestIdentifyIsNotCountedAsTraffic pins the structural split in
// NewMarkerDoorCounted. RequestsSeen()==0 is how a broken capture is
// detected; /identify is the tool asking about itself, so running
// `retrace check` against a live capture must not be able to disarm that
// verdict for the run it is inspecting.
func TestIdentifyIsNotCountedAsTraffic(t *testing.T) {
	fired := 0
	srv := httptest.NewServer(NewMarkerDoorCounted(runs.Paths{RunDir: t.TempDir()}, nil, func() { fired++ }))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/identify")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if fired != 0 {
		t.Fatalf("onAdmitted fired %d time(s) for /identify, want 0 — a supervision probe is not app traffic", fired)
	}

	// The counter must still work for a real marker on the same door, or
	// this test would pass just as well against a hook that never fires.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/group", strings.NewReader(`{"name":"checkout"}`))
	req.Header.Set("Content-Type", "application/json")
	mresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mresp.Body.Close()
	if fired != 1 {
		t.Fatalf("onAdmitted fired %d time(s) for a real marker, want 1", fired)
	}
}

// TestIdentifyIsGuarded: the endpoint discloses a pid and a run id, so it
// sits behind the same guard the state-changing markers do — loopback is
// not a defence against a browser tab.
func TestIdentifyIsGuarded(t *testing.T) {
	srv := httptest.NewServer(NewMarkerDoor(runs.Paths{RunDir: t.TempDir()}, nil))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/identify", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site /identify status = %d, want 403", resp.StatusCode)
	}
}

// TestIdentifyReportsAnUnreadableOwnerRecord: "not a retrace run dir" and
// "a run dir this build cannot parse" must not look alike — only the
// second is a reason to go read the file.
func TestIdentifyReportsAnUnreadableOwnerRecord(t *testing.T) {
	dir := t.TempDir()
	p := runs.Paths{RunDir: dir}
	if err := os.WriteFile(p.RunningPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewMarkerDoor(p, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/identify")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["tool"] != "retrace" {
		t.Errorf("tool = %v, want retrace", got["tool"])
	}
	if got["error"] == nil || got["error"] == "" {
		t.Error("an unreadable owner record was reported as no owner at all")
	}
}
