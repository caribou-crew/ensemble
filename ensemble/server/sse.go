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
func (s *server) handleTrafficStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	since := parseUint(r.URL.Query().Get("since"))
	ch, cancel := s.Rec.Subscribe(since)
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
		case h, open := <-ch:
			if !open {
				return
			}
			b, err := json.Marshal(h)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: hop\ndata: %s\n\n", b); err != nil {
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
