/// <reference types="vitest/config" />
import { defineConfig } from 'vite';

// The fixture is tested against a fake page object, not a real browser — no
// DOM environment needed.
export default defineConfig({ test: { environment: 'node' } });
