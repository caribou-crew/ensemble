// Pretty-prints a hop payload body: JSON when it parses, raw text
// otherwise, with a `truncated` banner when the capture hit the size cap
// (core/trace/redact.go's Redactor.Payload). Body values redaction has
// scrubbed render with the masked `.redacted` style — see redaction.ts.
import { redactedTitle, splitRedacted } from '../redaction';
import './JsonView.css';

export interface JsonViewProps {
  body?: string;
  truncated?: boolean;
}

export default function JsonView({ body, truncated }: JsonViewProps) {
  if (!body) {
    return <div className="json-view json-view--empty">no body</div>;
  }

  let pretty: string | null = null;
  try {
    pretty = JSON.stringify(JSON.parse(body), null, 2);
  } catch {
    pretty = null;
  }
  const text = pretty ?? body;
  const segments = splitRedacted(text);

  return (
    <div className="json-view">
      {truncated && (
        <div className="json-view__truncated">truncated — body exceeded the capture limit</div>
      )}
      <pre className={`json-view__pre${pretty ? '' : ' json-view__pre--raw'}`}>
        {segments.map((seg, i) =>
          seg.redacted ? (
            <span key={i} className="redacted" title={redactedTitle(seg.text)}>
              {seg.text}
            </span>
          ) : (
            <span key={i}>{seg.text}</span>
          ),
        )}
      </pre>
    </div>
  );
}
