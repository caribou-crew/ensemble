import { describe, expect, it } from 'vitest';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import HopTable from './HopTable';
import { CLIENT_IDENTITY_TITLE, clientIdentity } from './attribution';
import type { Hop } from '../api/types';

const base: Hop = {
  schema: 'ensemble/1',
  seq: 1,
  to: 'edge',
  method: 'GET',
  path: '/products',
  status: 200,
  t: { start: '2026-01-01T14:32:07.123Z', doneMs: 5 },
};

function render(hop: Hop): string {
  return renderToStaticMarkup(
    createElement(HopTable, { hops: [hop], selectedSeq: null, onSelectHop: () => {} }),
  );
}

describe('client identity in the traffic view', () => {
  it('shows the client application on an entry hop', () => {
    const markup = render({ ...base, client: 'web' });
    expect(markup).toContain('hop-table__client');
    expect(markup).toContain('>web<');
  });

  it('renders nothing when the hop carries no client', () => {
    // Most traffic has no client header. An empty badge on every row would
    // train the reader to ignore the column that matters on the rows that do.
    const markup = render(base);
    expect(markup).not.toContain('hop-table__client');
  });

  it('renders nothing for a blank client rather than an empty badge', () => {
    // A header that arrived present-but-empty is not an identity. The Go side
    // cannot produce this (clientIdentity skips empty values), which is
    // exactly why the UI must not depend on that staying true.
    expect(render({ ...base, client: '' })).not.toContain('hop-table__client');
    expect(clientIdentity({ ...base, client: '' })).toBeNull();
  });

  it('keeps the client badge distinct from the caller placeholder', () => {
    // The route cell renders the literal word "client" for an entry hop with
    // no caller, and "client" is ALSO what a malformed identity is recorded
    // as. Three different facts, and rendering the identity in the arrow's
    // left slot would collapse them into one word.
    const markup = render({ ...base, client: 'web' });
    expect(markup).toContain('client → edge');
    expect(markup).toContain('hop-table__client');
  });

  it('shows a real caller and the client side by side', () => {
    // An internal hop that forwards the client header carries both facts, and
    // they are not interchangeable: `from` is the calling service, `client` is
    // the front-end that started the whole chain.
    const markup = render({ ...base, from: 'storefront-bff', to: 'order', client: 'web' });
    expect(markup).toContain('storefront-bff → order');
    expect(markup).toContain('>web<');
  });

  it('explains what the badge means rather than showing a bare label', () => {
    // `client` is self-declared by the app, not derived from trace context. A
    // reader who takes it for a topology fact will read the graph wrong.
    const markup = render({ ...base, client: 'web' });
    expect(markup).toContain(CLIENT_IDENTITY_TITLE.slice(0, 40));
  });

  it('renders the fallback value as an ordinary identity', () => {
    // A malformed header is recorded as the literal "client" by the proxy.
    // The UI must show it — it is the visible symptom of a misconfigured app,
    // and hiding it is how that app never gets fixed.
    const markup = render({ ...base, client: 'client' });
    expect(markup).toContain('hop-table__client');
  });
});
