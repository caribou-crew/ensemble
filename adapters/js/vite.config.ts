/// <reference types="vitest/config" />
import { defineConfig } from 'vite';

// retrace-js is a Node package (fs, path, fetch) — no DOM environment needed,
// unlike the design-system's happy-dom config.
export default defineConfig({ test: { environment: 'node' } });
