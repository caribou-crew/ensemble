/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'happy-dom',
  },
  build: {
    // retrace/serve/ui embeds this directory verbatim via go:embed — see
    // retrace/serve/ui/ui.go. emptyOutDir keeps stale assets from a previous
    // build (different hashed filenames) out of the embed.
    outDir: '../../retrace/serve/ui/dist',
    emptyOutDir: true,
  },
  server: {
    // Dev-mode single-origin illusion: `retrace serve` binds 127.0.0.1:4800
    // by default (cmd_serve.go's defaultServeAddr), so proxying /api here
    // means the dev UI talks to the review server exactly the way the built,
    // embedded bundle does.
    proxy: {
      '/api': 'http://127.0.0.1:4800',
    },
  },
});
