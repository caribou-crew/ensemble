/// <reference types="vitest/config" />
import { defineConfig } from 'vite';

// Companion config for src/__guardProbes/*.probe.ts. Those files deliberately make an
// unmocked socket attempt, so the suite-wide guard in src/testSetup.ts MUST fail every one
// of them — which means they can never be part of the normal run (`*.test.ts`) without
// failing it by design. `src/testSetup.guard.test.ts` runs them through this config as a
// child vitest process and asserts they all fail; that is what turns "the guard observes
// attempts from these three windows" from a claim in a comment into a checked property.
export default defineConfig({
  test: {
    environment: 'happy-dom',
    include: ['src/__guardProbes/*.probe.ts'],
    setupFiles: ['./src/testSetup.ts'],
  },
});
