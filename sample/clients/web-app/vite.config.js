import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The port lives here rather than in the `dev` script so a SECOND dev server
// can run alongside the one `ensemble up` starts. A retrace recording needs
// its own: VITE_EDGE_URL is substituted into the bundle at transform time,
// and the recording run has to point at $RETRACE_PROXY_URL instead of
// edge-gw's proxy port. See playwright.config.js.
//
// VITE_PORT, not PORT: every other service in sample/ensemble.yaml is
// configured with a PORT of its own, and a name that generic collides the
// first time this app is started by something that sets one.
export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    port: Number(process.env.VITE_PORT) || 5173,
    strictPort: true,
  },
});
