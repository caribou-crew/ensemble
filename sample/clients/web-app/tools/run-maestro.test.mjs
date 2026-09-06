import assert from 'node:assert/strict';
import { test } from 'node:test';
import { spawn } from 'node:child_process';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createServer } from 'node:net';
import { createServer as createHTTPServer } from 'node:http';
import { fileURLToPath } from 'node:url';

const runner = fileURLToPath(new URL('./run-maestro.mjs', import.meta.url));
async function unusedPort() {
  const server = createServer();
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const port = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return port;
}
function execute(env) {
  const cleanEnv = { ...process.env };
  for (const key of Object.keys(cleanEnv)) if (key.startsWith('RETRACE_')) delete cleanEnv[key];
  const child = spawn(process.execPath, [runner], { env: { ...cleanEnv, ENSEMBLE_API: 'http://127.0.0.1:1', BREW_SETUP_EDGE_URL: 'http://127.0.0.1:1', ...env }, stdio: ['ignore', 'pipe', 'pipe'], timeout: 15000 });
  let output = '';
  child.stdout.on('data', (data) => { output += data; });
  child.stderr.on('data', (data) => { output += data; });
  return new Promise((resolve) => child.on('close', (code) => resolve({ code, output })));
}

test('a partial recording handshake refuses before starting a browser', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'brew-maestro-handshake-'));
  try {
    await writeFile(join(dir, 'maestro'), '#!/usr/bin/env node\nprocess.exit(0);\n', { mode: 0o755 });
    const result = await execute({ RETRACE_RUN_DIR: join(dir, 'run'), PATH: dir + ':' + process.env.PATH, BREW_TEST_PORT: String(await unusedPort()) });
    assert.notEqual(result.code, 0);
    assert.match(result.output, /incomplete.*handshake/i);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('recorded app uses the recording proxy and a failed Maestro child releases the app port', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'brew-maestro-test-'));
  const port = await unusedPort();
  const fixtures = createHTTPServer((req, res) => {
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify(req.url === '/api/seed/baseline'
      ? { ok: true, results: [{ ok: true }] }
      : { user_id: req.url.split('/')[2], items: [] }));
  });
  await new Promise((resolve) => fixtures.listen(0, '127.0.0.1', resolve));
  const fixtureURL = `http://127.0.0.1:${fixtures.address().port}`;
  try {
    // The external browser runner fails after observing what the real Vite
    // server serves. Omitting VITE_EDGE_URL forwarding breaks this assertion.
    await writeFile(join(dir, 'maestro'), `#!/usr/bin/env node\n
const args = process.argv.slice(2);
const value = (name) => args.find((arg) => arg.startsWith(name + '='))?.slice(name.length + 1);
const response = await fetch(value('BREW_APP_URL') + '/src/api.js');
const body = await response.text();
if (!body.includes('http://127.0.0.1:49871')) process.exit(91);
if (value('RETRACE_MARKER_URL') !== 'http://127.0.0.1:49872') process.exit(92);
process.exit(42);
`, { mode: 0o755 });
    const result = await execute({
      PATH: dir + ':' + process.env.PATH,
      BREW_TEST_PORT: String(port),
      RETRACE_RUN_DIR: join(dir, 'run'),
      RETRACE_PROXY_URL: 'http://127.0.0.1:49871',
      RETRACE_MARKER_URL: 'http://127.0.0.1:49872',
      ENSEMBLE_API: fixtureURL,
      BREW_SETUP_EDGE_URL: fixtureURL,
    });
    assert.equal(result.code, 42, result.output);
    const server = createServer();
    await new Promise((resolve, reject) => { server.once('error', reject); server.listen(port, '127.0.0.1', resolve); });
    await new Promise((resolve) => server.close(resolve));
  } finally {
    await new Promise((resolve) => fixtures.close(resolve));
    await rm(dir, { recursive: true, force: true });
  }
});

test('a successful Maestro process without every screenshot refuses the capture', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'brew-maestro-shots-'));
  const fixtures = createHTTPServer((req, res) => {
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify(req.url === '/api/seed/baseline'
      ? { ok: true, results: [{ ok: true }] }
      : { user_id: req.url.split('/')[2], items: [] }));
  });
  await new Promise((resolve) => fixtures.listen(0, '127.0.0.1', resolve));
  const fixtureURL = `http://127.0.0.1:${fixtures.address().port}`;
  try {
    await writeFile(join(dir, 'maestro'), `#!/usr/bin/env node\n
const { writeFileSync } = require('node:fs');
const arg = process.argv.find((arg) => arg.startsWith('BREW_SHOTS_DIR='));
const shots = arg.slice('BREW_SHOTS_DIR='.length);
// A valid catalog image cannot stand in for the missing cart checkpoint.
writeFileSync(shots + '/catalog.png', Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aSj8AAAAASUVORK5CYII=', 'base64'));
`, { mode: 0o755 });
    const result = await execute({
      PATH: dir + ':' + process.env.PATH,
      BREW_TEST_PORT: String(await unusedPort()),
      RETRACE_RUN_DIR: join(dir, 'run'),
      RETRACE_PROXY_URL: 'http://127.0.0.1:49871',
      RETRACE_MARKER_URL: 'http://127.0.0.1:49872',
      ENSEMBLE_API: fixtureURL,
      BREW_SETUP_EDGE_URL: fixtureURL,
    });
    assert.notEqual(result.code, 0, result.output);
    assert.match(result.output, /missing or empty Maestro screenshot: cart\.png/);
  } finally {
    await new Promise((resolve) => fixtures.close(resolve));
    await rm(dir, { recursive: true, force: true });
  }
});
