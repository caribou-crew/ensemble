/// <reference types="vitest/config" />
import { defineConfig } from 'vite';

// The design system ships source, not a build — this config exists only so
// vitest has a happy-dom environment for the hook test.
export default defineConfig({ test: { environment: 'happy-dom' } });
