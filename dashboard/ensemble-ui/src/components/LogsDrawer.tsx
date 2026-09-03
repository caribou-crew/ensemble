// Right-docked drawer for a single service's live log — the Services tab equivalent of
// TraceDrawer, both built on the shared Drawer shell.
import Drawer from './Drawer';
import LogPane from './LogPane';
import './LogsDrawer.css';

export default function LogsDrawer({ name, onClose }: { name: string | null; onClose: () => void }) {
  return (
    <Drawer
      open={name !== null}
      onClose={onClose}
      classPrefix="logs-drawer"
      ariaLabel={name ? `logs for ${name}` : 'logs'}
      header={<span className="logs-drawer__title">{name} — logs</span>}
    >
      {name && <LogPane name={name} />}
    </Drawer>
  );
}
