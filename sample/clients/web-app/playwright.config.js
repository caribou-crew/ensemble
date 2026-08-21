import { defineConfig } from '@playwright/test';

// These tests exercise the real backend (edge-gw -> storefront-bff ->
// order-svc -> ...), not a mock — start the sample stack with `ensemble up
// -c ../../ensemble.yaml --profile full` (checkout needs order-svc) before
// running `npm test`. reuseExistingServer means this also works standalone
// against a bare `npm run dev`, minus the checkout assertions.
export default defineConfig({
  testDir: './tests',
  timeout: 30000,
  fullyParallel: false,
  use: {
    baseURL: 'http://127.0.0.1:5173',
  },
  webServer: {
    command: 'npm run dev',
    url: 'http://127.0.0.1:5173',
    reuseExistingServer: true,
    timeout: 30000,
  },
});
