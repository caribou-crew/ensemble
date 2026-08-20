// EventSource wrapper for GET /api/traffic/stream. The endpoint sends
// `event: hop` frames (one Hop JSON object each) plus `: heartbeat` comment
// lines every 15s that EventSource ignores on its own — nothing to handle
// here. Hops carry a monotonic `seq`; a dropped connection reconnects with
// the last-seen seq as `since` so the resumed stream picks up without gaps
// or a full re-send of everything already delivered.
import type { Hop } from './types';

const RECONNECT_DELAY_MS = 1000;

/**
 * Subscribes to the live hop stream starting at `since` (pass 0, or the
 * last seq already held locally, to avoid replaying it). `onHop` is called
 * once per hop, in delivery order. On the connection's `error` event the
 * source is closed and, after a 1s backoff, reopened with `since` advanced
 * to the last seq actually observed. Returns an unsubscribe closure that
 * closes the current connection and cancels any pending reconnect — safe
 * to call at any point, including mid-backoff.
 */
export function subscribeHops(since: number, onHop: (hop: Hop) => void): () => void {
  let closed = false;
  let source: EventSource | null = null;
  let lastSeq = since;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  function connect(cursor: number) {
    if (closed) return;
    const es = new EventSource(`/api/traffic/stream?since=${cursor}`);
    source = es;

    es.addEventListener('hop', (evt) => {
      const raw = (evt as MessageEvent<string>).data;
      let hop: Hop;
      try {
        hop = JSON.parse(raw) as Hop;
      } catch {
        // Malformed frame — drop it rather than crash the whole stream.
        return;
      }
      lastSeq = hop.seq;
      onHop(hop);
    });

    es.onerror = () => {
      if (closed) return;
      es.close();
      reconnectTimer = setTimeout(() => connect(lastSeq), RECONNECT_DELAY_MS);
    };
  }

  connect(since);

  return () => {
    closed = true;
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    source?.close();
  };
}
