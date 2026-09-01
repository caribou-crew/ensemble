package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// heartbeatInterval keeps the SSE connection alive through idle proxies —
// a comment line, not an event, so it's invisible to EventSource consumers.
const heartbeatInterval = 15 * time.Second

// handleTrafficStream serves GET /api/traffic/stream?since=<seq>: replays
// retained hops with Seq > since, then streams live ones as they're
// recorded, honoring client disconnect via the request context.
//
// Two event names share the stream: `hop` is a fresh hop (a new Seq), and
// `hop.updated` is a finalization re-delivering a Seq already sent — a
// streaming hop closing with its duration and final body. Consumers upsert
// by seq on the latter; a consumer that only listens for `hop` (any
// pre-change client) simply keeps the headers-time snapshot, which is the
// compatible degradation.
func (s *server) handleTrafficStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	since := parseUint(r.URL.Query().Get("since"))
	ch, _, cancel := s.Rec.Subscribe(since)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			b, err := json.Marshal(ev.Hop)
			if err != nil {
				continue
			}
			event := "hop"
			if ev.Updated {
				event = "hop.updated"
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
