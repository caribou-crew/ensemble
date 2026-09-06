#!/usr/bin/env node
// Host-side bridge: Vite receives the recording edge at transform time;
// Maestro receives retrace's handshake explicitly as flow parameters.
import { spawn } from 'node:child_process';
import { mkdir, mkdtemp, stat } from 'node:fs/promises';
import { isAbsolute, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';
import { resetFixtures } from './reset-fixtures.mjs';

const appDir = fileURLToPath(new URL('../', import.meta.url));
const sampleDir = fileURLToPath(new URL('../../../', import.meta.url));

async function main() {
  const keys = ['RETRACE_RUN_DIR', 'RETRACE_PROXY_URL', 'RETRACE_MARKER_URL'];
  const recording = keys.some((key) => process.env[key]);
  if (recording && !keys.every((key) => process.env[key])) {
    throw new Error('incomplete retrace handshake: RETRACE_RUN_DIR, RETRACE_PROXY_URL and RETRACE_MARKER_URL must be supplied together');
  }
  if (!recording && /^(1|true|yes|on)$/i.test(process.env.RETRACE_STRICT || '')) {
    throw new Error('incomplete retrace handshake: strict mode requires an active retrace run');
  }
  if (recording && !isAbsolute(process.env.RETRACE_RUN_DIR)) {
    throw new Error('RETRACE_RUN_DIR must be an absolute path');
  }
  const port = Number(process.env.BREW_TEST_PORT || 5174);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('BREW_TEST_PORT must be a port between 1 and 65535');
  const edgeURL = process.env.RETRACE_PROXY_URL || process.env.VITE_EDGE_URL || 'http://127.0.0.1:9080';
  await mkdir(join(sampleDir, '.retrace'), { recursive: true });
  const output = process.env.RETRACE_RUN_DIR || await mkdtemp(join(sampleDir, '.retrace', 'maestro-'));
  const shots = join(output, 'shots');
  const report = join(output, 'report');
  await mkdir(shots, { recursive: true });
  await mkdir(report, { recursive: true });

  const controller = new AbortController();
  let interrupted = false;
  const onSignal = () => { interrupted = true; controller.abort(); };
  process.once('SIGINT', onSignal);
  process.once('SIGTERM', onSignal);
  let server;
  try {
    await resetFixtures();
    server = await createServer({
      root: appDir,
      define: { 'import.meta.env.VITE_EDGE_URL': JSON.stringify(edgeURL) },
      server: { host: '127.0.0.1', port, strictPort: true },
    });
    await server.listen();
    if (controller.signal.aborted) throw new Error('Maestro run interrupted');
    const parameters = {
      BREW_APP_URL: `http://127.0.0.1:${port}`,
      BREW_SHOTS_DIR: shots,
      RETRACE_MARKER_URL: process.env.RETRACE_MARKER_URL || '',
      RETRACE_STRICT: recording ? '1' : '0',
    };
    const args = ['test', '--headless', '--debug-output', report, '--format', 'JUNIT', '--output', join(report, 'results.xml')];
    for (const [key, value] of Object.entries(parameters)) args.push('-e', `${key}=${value}`);
    args.push(join(appDir, 'maestro', 'checkout.yaml'));
    console.error(`brew: Maestro evidence: ${output}`);
    const code = await new Promise((resolve, reject) => {
      const child = spawn('maestro', args, { cwd: appDir, stdio: 'inherit', signal: controller.signal });
      child.once('error', reject);
      child.once('close', (status) => resolve(status ?? 1));
    });
    if (code === 0 && !interrupted) {
      for (const name of ['catalog', 'cart']) {
        const file = await stat(join(shots, `${name}.png`)).catch(() => null);
        if (!file?.isFile() || file.size === 0) throw new Error(`missing or empty Maestro screenshot: ${name}.png`);
      }
    }
    process.exitCode = interrupted ? 130 : code;
  } finally {
    await server?.close();
    process.removeListener('SIGINT', onSignal);
    process.removeListener('SIGTERM', onSignal);
  }
}

main().catch((error) => {
  console.error(`brew: ${error.message}`);
  process.exitCode = error.name === 'AbortError' ? 130 : 1;
});
