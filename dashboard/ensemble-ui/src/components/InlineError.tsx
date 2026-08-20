// "A sub-resource failed while the shell is fine" had three separate treatments across the
// dashboard — a badge in EntityView, a bare div in InspectorView, a bare span in TopologyView
// (final review M5). The top-level error state (Badge tone="red" + a `<view>--error` wrapper)
// was already perfectly consistent across all five views; this is that same shape, sized down
// for the sub-resource case, so every schema/rows/trace/panel error reads the same way.
import { Badge } from '@ensemble/design-system';
import './InlineError.css';

export default function InlineError({ message, className }: { message: string; className?: string }) {
  return (
    <div className={`inline-error${className ? ` ${className}` : ''}`}>
      <Badge tone="red">error</Badge>
      <span>{message}</span>
    </div>
  );
}
