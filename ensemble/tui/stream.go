package tui

import (
	"context"
	"time"

	"github.com/caribou-crew/ensemble/core/trace"
)

// streamReconnectDelay is how long StreamTraffic waits after a dropped or
// failed connection before retrying — matches
// dashboard/ensemble-ui/src/api/sse.ts's fixed 1s reconnect delay. A var,
// not a const, so tests can shorten it (see setStreamReconnectDelayForTest)
// instead of a reconnect test taking a full second per attempt.
var streamReconnectDelay = time.Second

// StreamTraffic wraps apiClient.TrafficStream with reconnect-on-drop: it
// keeps a single connection open, and whenever it closes (server restart,
// network blip, any error) it waits streamReconnectDelay and reconnects
// with since advanced to the last hop's Seq, so a reconnect doesn't
// replay hops already delivered. It keeps going until ctx is canceled, at
// which point the returned channel is closed.
func StreamTraffic(ctx context.Context, client apiClient, since uint64) <-chan trace.Hop {
	out := make(chan trace.Hop)
	go func() {
		defer close(out)
		cursor := since
		for {
			if ctx.Err() != nil {
				return
			}
			hops, err := client.TrafficStream(ctx, cursor)
			if err != nil {
				if !sleepOrDone(ctx, streamReconnectDelay) {
					return
				}
				continue
			}
			for h := range hops {
				// Advance only: a hop.updated frame re-delivers an OLD seq
				// (a streaming hop finalizing), and regressing the cursor to
				// it would make the next reconnect replay everything since.
				if h.Seq > cursor {
					cursor = h.Seq
				}
				select {
				case out <- h:
				case <-ctx.Done():
					return
				}
			}
			// hops closed: connection ended (server drop, EOF). Reconnect
			// unless ctx is what caused it.
			if !sleepOrDone(ctx, streamReconnectDelay) {
				return
			}
		}
	}()
	return out
}

// sleepOrDone waits d, returning false early (without sleeping the full
// duration) if ctx is canceled first — the signal to give up rather than
// reconnect.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
