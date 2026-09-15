package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// heartbeatInterval keeps the SSE connection alive through idle proxies —
// a comment line, not an event, so it's invisible to EventSource consumers.
const heartbeatInterval = 15 * time.Second

// handleTrafficStream serves GET /api/traffic/stream?since=<seq>&client=:
// replays retained hops with Seq > since, then streams live ones as they're
// recorded, honoring client disconnect via the request context. client
// narrows the stream to one originating client application (or, with
// trace.UnattributedClient, to hops belonging to none).
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
	client := r.URL.Query().Get("client")

	// Seeded from the ring before subscribing so a replayed hop, and any
	// live hop belonging to a chain already under way, resolves against the
	// traces already recorded rather than only against what this connection
	// happens to witness. Folded forward as events arrive (ObserveClient)
	// for chains that start after the subscription.
	//
	// Attribution on a LIVE stream is exact for chains that propagate the
	// identity in baggage — every such hop carries it, and the index is not
	// consulted. It is best-effort for a chain that dropped baggage: hops
	// are recorded inner-first, so an inner hop can arrive before the entry
	// hop that would name its client, and a filtered stream has no way to
	// re-deliver it later. That hop reads as unattributed. This is why the
	// identity propagates at capture (see core/trace.BaggageClient) instead
	// of being inferred here.
	clients := map[string]string{}
	if client != "" {
		clients = trace.ClientIndex(s.Rec.Snapshot())
	}

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
			trace.ObserveClient(clients, ev.Hop)
			if !trace.MatchesClient(ev.Hop, clients, client) {
				continue
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
