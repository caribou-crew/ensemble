// Hand-authored topology and trace fixtures, standing in for real `GET /api/topology` and
// `GET /api/traces/{id}` responses so the pure layout modules can be tested without a running
// ensemble stack. Ported in shape from local-stack/web/src/topology/fixtures.ts (which
// checked in real `stack.sh topology --json` output); ensemble's shape is coarser — only
// service/database/stub categories exist, so this fixture is authored rather than captured,
// sized to still exercise layout.ts's bundling threshold (BUNDLE_MIN = 3) and traceLayout's
// branching/racy-timestamp cases.
import type { Hop, Topology } from '../api/types';

export const SAMPLE_TOPOLOGY: Topology = {
  nodes: [
    { name: 'edge-gateway', category: 'service', status: 'healthy', entry: true },
    { name: 'orders', category: 'service', status: 'healthy' },
    { name: 'payments', category: 'service', status: 'healthy' },
    { name: 'accounts', category: 'service', status: 'healthy' },
    { name: 'inventory', category: 'service', status: 'healthy' },
    { name: 'notifications', category: 'service', status: 'healthy' },
    { name: 'orders-db', category: 'database', status: 'healthy' },
    { name: 'payments-db', category: 'database', status: 'healthy' },
    { name: 'accounts-db', category: 'database', status: 'healthy' },
    { name: 'email-stub', category: 'stub', status: 'static' },
    { name: 'sms-stub', category: 'stub', status: 'static' },
    { name: 'push-stub', category: 'stub', status: 'static' },
    { name: 'fraud-stub', category: 'stub', status: 'static' },
  ],
  edges: [
    { from: 'edge-gateway', to: 'orders' },
    { from: 'edge-gateway', to: 'payments' },
    { from: 'edge-gateway', to: 'accounts' },
    { from: 'edge-gateway', to: 'notifications' },
    { from: 'orders', to: 'orders-db' },
    { from: 'orders', to: 'inventory' },
    { from: 'payments', to: 'payments-db' },
    // Exactly two stub targets: below BUNDLE_MIN, must stay individual edges.
    { from: 'payments', to: 'fraud-stub' },
    { from: 'payments', to: 'email-stub' },
    { from: 'accounts', to: 'accounts-db' },
    // Exactly three stub targets: at BUNDLE_MIN, must collapse to one bundled edge.
    { from: 'notifications', to: 'email-stub' },
    { from: 'notifications', to: 'sms-stub' },
    { from: 'notifications', to: 'push-stub' },
  ],
};

function hop(n: number, from: string | undefined, to: string): Hop {
  return {
    schema: 'ensemble/1',
    seq: n,
    traceId: 'trace-fixture',
    from,
    to,
    method: 'GET',
    path: `/step/${n}`,
    status: 200,
    t: { start: `2026-08-13T00:00:0${n}.000Z`, doneMs: 10 },
  };
}

/** undefined → edge-gateway → orders → orders-db, a straight chain. The first hop's `from`
    is left undefined (as a real root hop's would be — nothing calls the entry) so
    traceLayout's synthetic-"client" fallback gets exercised, not just the fixture's naming
    convention. */
export const LINEAR_HOPS: Hop[] = [
  hop(1, undefined, 'edge-gateway'),
  hop(2, 'edge-gateway', 'orders'),
  hop(3, 'orders', 'inventory'),
  hop(4, 'inventory', 'orders-db'),
];

/** Same chain, but inventory fans out to orders-db and payments-db, and calls orders-db
    twice. */
export const BRANCHING_HOPS: Hop[] = [
  hop(1, undefined, 'edge-gateway'),
  hop(2, 'edge-gateway', 'orders'),
  hop(3, 'orders', 'inventory'),
  hop(4, 'inventory', 'orders-db'),
  hop(5, 'inventory', 'payments-db'),
  hop(6, 'inventory', 'orders-db'),
];
