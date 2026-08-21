import { Badge } from '@ensemble/design-system';
import type { HopDiff, Route } from '../api/types';
import './HopDeltaList.css';

const routeKey = (r: Route) => `${r.method} ${r.path} → ${r.to}`;

function RouteLine({ route, kind }: { route: Route; kind: 'new' | 'gone' }) {
  return (
    <li className={`hop-route hop-route--${kind}`}>
      <Badge tone={kind === 'new' ? 'green' : 'red'}>{kind === 'new' ? 'new' : 'gone'}</Badge>
      <code>
        {route.method} {route.path}
      </code>
      <span className="hop-route__to">→ {route.to}</span>
      {route.via && route.via.length > 0 ? (
        <span className="hop-route__via">via {route.via.join(' → ')}</span>
      ) : null}
    </li>
  );
}

/**
 * The hop plane: routes that appeared or vanished between the two runs, and
 * the services whose call counts deviated.
 *
 * Only DEVIATING service counts are listed. Every service in a healthy run
 * has a count, and printing all of them would bury the two that moved.
 */
export default function HopDeltaList({ hops }: { hops: HopDiff }) {
  const deviating = hops.serviceCounts.filter((s) => s.deviates);
  const nothing =
    hops.newRoutes.length === 0 && hops.goneRoutes.length === 0 && deviating.length === 0;

  if (nothing) {
    return (
      <p className="hop-empty">
        No route appeared or vanished, and no service's call count deviated.
      </p>
    );
  }

  return (
    <div className="hop-delta">
      {hops.newRoutes.length > 0 || hops.goneRoutes.length > 0 ? (
        <ul className="hop-routes">
          {hops.newRoutes.map((r) => (
            <RouteLine key={`new:${routeKey(r)}`} route={r} kind="new" />
          ))}
          {hops.goneRoutes.map((r) => (
            <RouteLine key={`gone:${routeKey(r)}`} route={r} kind="gone" />
          ))}
        </ul>
      ) : null}
      {deviating.length > 0 ? (
        <table className="hop-counts">
          <thead>
            <tr>
              <th>service</th>
              <th>reference</th>
              <th>this run</th>
            </tr>
          </thead>
          <tbody>
            {deviating.map((s) => (
              <tr key={s.service}>
                <td>
                  <code>{s.service}</code>
                </td>
                <td>{s.a}</td>
                <td>{s.b}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
    </div>
  );
}
