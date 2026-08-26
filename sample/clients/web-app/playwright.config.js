import { defineConfig } from '@playwright/test';

// One config, two modes.
//
// `pnpm run e2e` on its own drives the dev server `ensemble up` already
// started (5173), whose bundle points at edge-gw's proxy — the same thing
// you get by opening the app yourself. These tests exercise the real
// backend (edge-gw -> storefront-bff -> order -> ...), not a mock, so the
// sample stack has to be up: `ensemble up -c ../../ensemble.yaml`.
//
// Under `retrace run` (RETRACE_PROXY_URL is set) the app's calls have to go
// through retrace's recording edge instead. VITE_EDGE_URL is substituted
// into the bundle at transform time, so pointing at the recording edge
// means a SECOND dev server on its own port — reusing 5173 would record
// nothing at all, because that bundle still calls 9080 directly and
// retrace's own capture-trust check would report `empty` rather than a
// diff. reuseExistingServer is off for the same reason: an existing server
// on this port is not necessarily pointed where this run needs it.
const proxyURL = process.env.RETRACE_PROXY_URL;
const port = proxyURL ? 5174 : 5173;

export default defineConfig({
  testDir: './tests',
  timeout: 30000,
  // The suite shares one cart per user id, and under `retrace run` both
  // tests write checkpoints and flow-part markers into a single run
  // directory — either alone is reason enough to keep this sequential.
  fullyParallel: false,
  use: {
    baseURL: `http://127.0.0.1:${port}`,
  },
  webServer: {
    command: 'pnpm run dev',
    url: `http://127.0.0.1:${port}`,
    env: {
      VITE_PORT: String(port),
      VITE_EDGE_URL: proxyURL || process.env.VITE_EDGE_URL || 'http://127.0.0.1:9080',
    },
    reuseExistingServer: !proxyURL,
    timeout: 30000,
  },
});
