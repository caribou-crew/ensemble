import { describe, expect, it } from 'vitest';
import { ensembleTrafficUrl } from './ensembleTrafficLink';
import type { Manifest } from './retraceTypes';

function manifest(ensemble?: { api: string; session: string }): Manifest {
  return { runId: 'run-7', ensemble } as unknown as Manifest;
}

describe('ensembleTrafficUrl', () => {
  it('addresses the run by the session it registered', () => {
    expect(ensembleTrafficUrl(manifest({ api: 'http://127.0.0.1:4700', session: 'run-7' }))).toBe(
      'http://127.0.0.1:4700/?view=traffic&session=run-7',
    );
  });

  it('tolerates a trailing slash on the recorded api url', () => {
    expect(ensembleTrafficUrl(manifest({ api: 'http://127.0.0.1:4700/', session: 'run-7' }))).toBe(
      'http://127.0.0.1:4700/?view=traffic&session=run-7',
    );
  });

  it('escapes a session id so it cannot alter the query', () => {
    expect(ensembleTrafficUrl(manifest({ api: 'http://x', session: 'a&view=services' }))).toBe(
      'http://x/?view=traffic&session=a%26view%3Dservices',
    );
  });

  // A standalone run has no control plane. Rendering a link anyway would
  // send a reviewer to a dead address, or to whatever stack happens to be
  // on the default port — another run's traffic under this run's heading.
  it('returns null for a run with no ensemble link', () => {
    expect(ensembleTrafficUrl(manifest())).toBeNull();
    expect(ensembleTrafficUrl(undefined)).toBeNull();
    expect(ensembleTrafficUrl(null)).toBeNull();
  });

  it('returns null for a half-filled link rather than a link to the whole stack', () => {
    expect(ensembleTrafficUrl(manifest({ api: 'http://127.0.0.1:4700', session: '' }))).toBeNull();
    expect(ensembleTrafficUrl(manifest({ api: '', session: 'run-7' }))).toBeNull();
  });
});
