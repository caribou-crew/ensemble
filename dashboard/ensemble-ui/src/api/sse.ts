// EventSource wrappers for the dashboard's two SSE feeds: GET
// /api/traffic/stream (`event: hop`) and GET /api/inspector/stream
// (`event: change`) — same framing for both (named-event frames plus a `:
// heartbeat` comment every 15s that EventSource ignores on its own,
// nothing to handle here). subscribeSSE holds the connect/reconnect
// mechanics shared by both; each public subscribe function only supplies
// the URL, event name, and per-frame parsing.
import type { ChangeEvent, Hop } from './types';

const RECONNECT_DELAY_MS = 1000;

/**
 * Opens an EventSource, listens for one named event, and reconnects 1s
 * after any `error` event — `urlFor()` is called fresh on every (re)connect
 * so a caller that closes over mutable cursor state (e.g. subscribeHops'
 * `lastSeq`) picks up its latest value on each reconnect. Returns an
 * unsubscribe closure that closes the current connection and cancels any
 * pending reconnect — safe to call at any point, including mid-backoff.
 */
function subscribeSSE(
  urlFor: () => string,
  eventName: string,
  onFrame: (data: string) => void,
  extraEvents?: Record<string, (data: string) => void>,
): () => void {
  let closed = false;
  let source: EventSource | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  function connect() {
    if (closed) return;
    const es = new EventSource(urlFor());
    source = es;

    es.addEventListener(eventName, (evt) => {
      onFrame((evt as MessageEvent<string>).data);
    });
    for (const [name, handler] of Object.entries(extraEvents ?? {})) {
      es.addEventListener(name, (evt) => {
        handler((evt as MessageEvent<string>).data);
      });
    }

    es.onerror = () => {
      if (closed) return;
      es.close();
      reconnectTimer = setTimeout(connect, RECONNECT_DELAY_MS);
    };
  }

  connect();

  return () => {
    closed = true;
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    source?.close();
  };
}

/**
 * Subscribes to the live hop stream starting at `since` (pass 0, or the
 * last seq already held locally, to avoid replaying it). `onHop` is called
 * once per hop, in delivery order. On reconnect, `since` is advanced to the
 * last seq actually observed.
 *
 * `onHopUpdated` (optional) receives `hop.updated` frames: a finalization
 * re-delivering a seq already sent — a streaming hop closing with its
 * duration and final body. Consumers upsert by seq; the reconnect cursor
 * never regresses to an updated hop's older seq.
 */
export function subscribeHops(
  since: number,
  onHop: (hop: Hop) => void,
  onHopUpdated?: (hop: Hop) => void,
): () => void {
  let lastSeq = since;
  const parse = (raw: string): Hop | null => {
    try {
      return JSON.parse(raw) as Hop;
    } catch {
      // Malformed frame — drop it rather than crash the whole stream.
      return null;
    }
  };
  return subscribeSSE(
    () => `/api/traffic/stream?since=${lastSeq}`,
    'hop',
    (raw) => {
      const hop = parse(raw);
      if (!hop) return;
      lastSeq = hop.seq;
      onHop(hop);
    },
    onHopUpdated && {
      'hop.updated': (raw) => {
        const hop = parse(raw);
        if (!hop) return;
        onHopUpdated(hop);
      },
    },
  );
}

/**
 * Subscribes to a service's log follow (GET /api/services/{name}/logs/stream,
 * `event: log`). The server replays a ~200-line tail as the first frame,
 * then streams appended chunks of complete lines; each frame arrives here
 * as plain text with EventSource having rejoined its lines with "\n". Like
 * the inspector stream there's no cursor — a reconnect replays the current
 * tail again, so consumers should treat every frame as append-only text and
 * cap their own buffer.
 */
export function subscribeServiceLog(name: string, onChunk: (chunk: string) => void): () => void {
  return subscribeSSE(
    () => `/api/services/${encodeURIComponent(name)}/logs/stream`,
    'log',
    onChunk,
  );
}

/**
 * Subscribes to the inspector's live change stream. Unlike traffic, the
 * server side (inspectHub) has no replay/backlog — it only fans out
 * changes observed while a subscriber is connected — so there's no cursor
 * to carry across a reconnect; a dropped connection just resumes watching
 * from whatever the poller sees next.
 */
export function subscribeChanges(onChange: (ev: ChangeEvent) => void): () => void {
  return subscribeSSE(
    () => '/api/inspector/stream',
    'change',
    (raw) => {
      let ev: ChangeEvent;
      try {
        ev = JSON.parse(raw) as ChangeEvent;
      } catch {
        return;
      }
      onChange(ev);
    },
  );
}
