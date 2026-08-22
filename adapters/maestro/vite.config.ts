/// <reference types="vitest/config" />
import { defineConfig } from 'vite';

// markerRequest is pure and network-free — no DOM environment needed.
export default defineConfig({ test: { environment: 'node' } });
