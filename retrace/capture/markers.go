package capture

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/caribou-crew/ensemble/core/httpguard"
	"github.com/caribou-crew/ensemble/retrace/runs"
)

// NewMarkerDoor is the HTTP face of flow-part markers, for runners that
// cannot write files (Maestro), plus the supervision endpoint that names
// the run holding this port. Routes are registered per-method: a
// method-less pattern would panic at registration against any "GET /"
// sibling, and the bare paths are registered explicitly so a POST is never
// answered with a subtree-redirect 301, which drops the body.
//
//	POST /group      start a named flow part
//	POST /group/end  end the current flow part
//	GET  /identify   who owns this port (see below; NOT counted as traffic)
//
// p is a runs.Paths, never a bare run-dir string: this handler is exactly
// the place a later caller would wire RETRACE_RUN_DIR or a request-derived
// value into, and a Paths is only obtainable from runs.PathsFor/runs.Create,
// both of which validate app/flow/runID against traversal.
func NewMarkerDoor(p runs.Paths, now func() time.Time) http.Handler {
	return NewMarkerDoorCounted(p, now, nil)
}

// NewMarkerDoorCounted is NewMarkerDoor plus an onAdmitted hook, called once
// per request the GUARD admits — after httpguard.Handler has let it
// through, and never for one the guard rejected. A cross-site POST, a
// rebound Host, an `Origin: null` therefore never count (verified: 403, and
// the hook does not fire).
//
// It is NOT a count of requests the ROUTER accepted, and must not be read
// as one. The hook sits between the guard and the mux, so everything the
// guard admits is counted whatever the mux then answers: a nameless-marker
// 400, a malformed-body 400 and a stray port probe's 404 all increment it.
// (This comment used to claim the opposite of that, naming the
// nameless-marker 400 as uncounted. The hook placement is correct and the
// sentence was wrong — capture.Assess's doc, written against the same
// mechanism, has always said so.)
//
// The placement is deliberate and load-bearing on that reading:
// Session.RequestsSeen()==0 is the signal Task 6 keys "the app never routed
// through us" on, and Session.WatchProxy's fallback leans on it too, so a
// guard-rejected request must not disarm either. The inflation the mux
// contributes is handled one layer up rather than here — Assess reads
// RequestsSeen in exactly one branch, where it can only demote `broken` to
// `degraded` and can never reach VerdictOK (pinned by
// TestInflatedRequestsSeenNeverReadsAsClean).
//
// So RequestsSeen > 0 means "something cleared the door's guard", NOT "real
// traffic flowed". Do not key a new decision on it as though it meant the
// second.
func NewMarkerDoorCounted(p runs.Paths, now func() time.Time, onAdmitted func()) http.Handler {
	if now == nil {
		now = time.Now
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /group", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name  string `json:"name"`
			Quiet bool   `json:"quiet"`
		}
		// A decode error is reported, not swallowed: a malformed marker body
		// and an empty one are different mistakes, and an adapter that is
		// silently posting garbage would otherwise look like an adapter that
		// is working.
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"marker body is not valid JSON: `+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Name) == "" {
			http.Error(w, `{"error":"group markers require a non-empty name"}`, http.StatusBadRequest)
			return
		}
		if err := runs.AppendGroupRecord(p, runs.GroupRecord{
			Phase: "start", Name: body.Name, TS: now(), Quiet: body.Quiet,
		}); err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /group/end", func(w http.ResponseWriter, r *http.Request) {
		if err := runs.AppendGroupRecord(p, runs.GroupRecord{Phase: "end", TS: now()}); err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// GET /identify answers "who holds this port". The incident it exists
	// for: a listener survives a killed capture, the next `retrace run` (or
	// `ensemble up`) reports the port as busy, and the only tool that could
	// name the holder was lsof — which gives a pid and nothing about which
	// RUN it belongs to.
	//
	// The answer is read from the run directory's own running.json at
	// request time rather than captured into this closure. A door that held
	// its own copy of the identity could disagree with the file after a
	// Finalize, and the whole point of the endpoint is to be the
	// authoritative answer. Reading it live also means this endpoint costs
	// the replay server (which opens a marker door but writes no owner
	// record) nothing but an honest ownerRecorded:false.
	identify := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// `tool` is answered before anything else can fail: proving the
		// listener is retrace at all is the first half of the question, and
		// it must not depend on the run directory being readable.
		body := map[string]any{"tool": "retrace", "ownerRecorded": false}
		owner, err := runs.ReadRunning(p)
		switch {
		case err != nil:
			// An unreadable owner record is reported, not swallowed: it is
			// the difference between "not a retrace run dir" and "a retrace
			// run dir this build cannot parse", and only one of those is a
			// reason to go looking at the file.
			body["error"] = err.Error()
		case owner != nil:
			body["ownerRecorded"] = true
			body["pid"] = owner.PID
			body["app"] = owner.App
			body["flow"] = owner.Flow
			body["runId"] = owner.RunID
			body["startedAt"] = owner.StartedAt
			if owner.ProxyURL != "" {
				body["proxyUrl"] = owner.ProxyURL
			}
			if owner.MarkerURL != "" {
				body["markerUrl"] = owner.MarkerURL
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	// onAdmitted wraps the MARKER routes only, never /identify. RequestsSeen
	// is the signal Task 6 keys "the app never routed through us" on, and
	// /identify is a supervision probe from `retrace check` — traffic the
	// tool makes about itself, not traffic the app made. Counting it would
	// let running `retrace check` against a live capture disarm the
	// broken-capture detection for that run, which is the one verdict the
	// counter exists to produce.
	//
	// The split is structural rather than a conditional inside the hook: an
	// `if r.URL.Path != "/identify"` guard would silently stop matching the
	// day a second non-traffic route is added.
	var counted http.Handler = mux
	if onAdmitted != nil {
		counted = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			onAdmitted()
			mux.ServeHTTP(w, r)
		})
	}
	outer := http.NewServeMux()
	outer.Handle("GET /identify", identify)
	outer.Handle("/", counted)

	// The SAME guard ensemble's control plane uses — not a copy of its
	// Sec-Fetch-Site check. Loopback keeps the network out but not a browser
	// tab, and the door is a state-changing POST endpoint on a predictable
	// port, so it needs the Host (DNS-rebinding) and Origin checks too, not
	// just the cross-site one. nil allowed-hosts = answer only as loopback.
	//
	// The guard stays outermost, so /identify is no more reachable from a
	// browser tab than the markers are: it discloses a pid and a run id.
	return httpguard.Handler(nil, outer)
}
