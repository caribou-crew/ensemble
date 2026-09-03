// One service's live log: subscribes to the SSE follow on mount (the server replays a
// ~200-line tail first, then streams appended lines — build output included), pins the
// scroll to the bottom as text arrives, and unsubscribes on unmount/close.
import { useEffect, useRef, useState } from 'react';
import { subscribeServiceLog } from '../api/sse';
import './LogPane.css';

// Lines kept in the buffer — a follow of a chatty service must not grow the DOM unbounded,
// and an SSE reconnect replays the tail (see subscribeServiceLog), so trimming from the top
// is always safe.
const LOG_PANE_MAX_LINES = 2000;

export default function LogPane({ name }: { name: string }) {
  const [text, setText] = useState('');
  const preRef = useRef<HTMLPreElement>(null);

  useEffect(() => {
    setText('');
    return subscribeServiceLog(name, (chunk) => {
      setText((prev) => {
        const next = prev ? `${prev}\n${chunk}` : chunk;
        const lines = next.split('\n');
        return lines.length > LOG_PANE_MAX_LINES ? lines.slice(-LOG_PANE_MAX_LINES).join('\n') : next;
      });
    });
  }, [name]);

  useEffect(() => {
    const el = preRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [text]);

  return (
    <pre ref={preRef} className="log-pane">
      {text || '(no log output yet)'}
    </pre>
  );
}
