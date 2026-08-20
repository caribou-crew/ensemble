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
// cannot write files (Maestro). Two routes, registered per-method: a
// method-less pattern would panic at registration against any "GET /"
// sibling, and the bare paths are registered explicitly so a POST is never
// answered with a subtree-redirect 301, which drops the body.
//
// p is a runs.Paths, never a bare run-dir string: this handler is exactly
// the place a later caller would wire RETRACE_RUN_DIR or a request-derived
// value into, and a Paths is only obtainable from runs.PathsFor/runs.Create,
// both of which validate app/flow/runID against traversal.
func NewMarkerDoor(p runs.Paths, now func() time.Time) http.Handler {
	return NewMarkerDoorCounted(p, now, nil)
}

// NewMarkerDoorCounted is NewMarkerDoor plus an onAdmitted hook, called once
// per request that reaches the router — i.e. AFTER httpguard.Handler has
// let it through, never for a request the guard rejected. A cross-site
// POST, a nameless-marker 400, or a stray port probe must never count as
// "traffic that reached retrace": Session.RequestsSeen()==0 is the signal
// Task 6 keys "the app never routed through us" on, and Session.WatchProxy's
// fallback leans on it too — counting rejected requests disarms both.
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
	// The SAME guard ensemble's control plane uses — not a copy of its
	// Sec-Fetch-Site check. Loopback keeps the network out but not a browser
	// tab, and the door is a state-changing POST endpoint on a predictable
	// port, so it needs the Host (DNS-rebinding) and Origin checks too, not
	// just the cross-site one. nil allowed-hosts = answer only as loopback.
	//
	// onAdmitted, when set, runs INSIDE the guard — between it and mux — so
	// it only fires for a request the guard actually let through.
	var inner http.Handler = mux
	if onAdmitted != nil {
		inner = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			onAdmitted()
			mux.ServeHTTP(w, r)
		})
	}
	return httpguard.Handler(nil, inner)
}
