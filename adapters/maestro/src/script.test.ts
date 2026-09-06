import * as fs from 'node:fs/promises';
import * as vm from 'node:vm';
import { describe, expect, it } from 'vitest';
import { markerRequest } from './index.js';

const source = await fs.readFile(new URL('../bin/retrace-maestro.js', import.meta.url), 'utf8');

interface MarkerPost {
  url: string;
  options: { headers: Record<string, string>; body: string };
}

function runScript(env: Record<string, string>, response = { ok: true, status: 204, body: '' }): MarkerPost[] {
  const posts: MarkerPost[] = [];
  vm.runInNewContext(source, {
    ...env,
    http: { post: (url: string, options: MarkerPost['options']) => {
      posts.push({ url, options });
      return response;
    } },
  });
  return posts;
}

describe('Maestro runScript entrypoint', () => {
  it('posts a group using flow globals and the native HTTP client without Node globals', () => {
    expect(runScript({ ARGS: 'group checkout', RETRACE_MARKER_URL: 'http://127.0.0.1:9999/' })).toEqual([
      { url: 'http://127.0.0.1:9999/group', options: {
        headers: { 'content-type': 'application/json' }, body: '{"name":"checkout"}',
      } },
    ]);
  });

  it('ends the current group without a name', () => {
    const posts = runScript({ ARGS: 'group --end', RETRACE_MARKER_URL: 'http://127.0.0.1:9999' });
    expect(posts).toHaveLength(1);
    expect(posts[0]).toMatchObject({ url: 'http://127.0.0.1:9999/group/end', options: { body: '{}' } });
  });

  it.each(['', '0', 'false', 'NO', ' off '])('is a silent no-op without a marker URL when strict is %j', (strict) => {
    expect(runScript({ ARGS: 'group checkout', RETRACE_STRICT: strict })).toEqual([]);
  });

  it('is a silent no-op when the handshake is entirely absent', () => {
    expect(runScript({ ARGS: 'group checkout' })).toEqual([]);
  });

  it.each(['1', 'true', 'YES', ' on ', 'enabled'])('matches Node strict-mode failures for %j', (strict) => {
    const env = { RETRACE_STRICT: strict };
    let expected: string | undefined;
    try { markerRequest(['group', 'checkout'], env); } catch (err) { expected = (err as Error).message; }
    expect(expected).toBeDefined();
    expect(() => runScript({ ...env, ARGS: 'group checkout' })).toThrow(expected);
  });

  it.each(['group', 'group .hidden', 'group ..', 'group cart/item', 'group cart\\item', 'group add to cart', 'group café', 'end'])('matches Node argument rejection for %j', (args) => {
    let expected: string | undefined;
    try { markerRequest(args.split(/\s+/), {}); } catch (err) { expected = (err as Error).message; }
    expect(expected).toBeDefined();
    expect(() => runScript({ ARGS: args })).toThrow(expected);
  });

  it('fails when the marker door rejects the POST', () => {
    expect(() => runScript(
      { ARGS: 'group checkout', RETRACE_MARKER_URL: 'http://127.0.0.1:9999' },
      { ok: false, status: 400, body: 'marker rejected' },
    )).toThrow(/400: marker rejected/);
  });

  it('propagates an HTTP transport failure', () => {
    expect(() => vm.runInNewContext(source, {
      ARGS: 'group checkout', RETRACE_MARKER_URL: 'http://127.0.0.1:9999',
      http: { post: () => { throw new Error('connection refused'); } },
    })).toThrow(/connection refused/);
  });
});
