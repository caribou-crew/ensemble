/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'happy-dom',
  },
  build: {
    // ensemble/server embeds this directory verbatim via go:embed — see
    // ensemble/server/ui/ui.go. emptyOutDir keeps stale assets from a
    // previous build (different hashed filenames) out of the embed.
    outDir: '../../ensemble/server/ui/dist',
    emptyOutDir: true,
  },
  server: {
    // Dev-mode single-origin illusion: the ensemble binary's control plane
    // (Task 2.x) listens on 127.0.0.1:4700; proxying /api here means the
    // dashboard talks to it exactly the way the built, embedded UI does.
    proxy: {
      '/api': 'http://127.0.0.1:4700',
    },
  },
});
