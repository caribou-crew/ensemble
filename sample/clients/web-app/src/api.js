// api.js talks to edge-gw — the sample stack's entry point — and nothing
// else. edge-gw's proxy stamps a fresh traceparent on any request that
// doesn't already carry one, so a plain browser fetch() is a valid trace
// root; this file has no tracing code of its own.
const EDGE_URL = import.meta.env.VITE_EDGE_URL || 'http://127.0.0.1:9080';
const AUTH_HEADER = 'Bearer demo-token';

// Which of the stack's front-ends this is. ensemble reads it off the entry
// hop with no configuration at all — x-source-client is one of the two
// headers checked by default — and shows it in the traffic view, so a stack
// with a web app and an admin app can tell whose call it is looking at.
//
// Lower-case and short because it is validated as an IDENTIFIER
// (^[a-z0-9][a-z0-9:-]{0,31}$): a value that fails is recorded as the
// literal "client" and the original is never stored, so nothing a browser
// puts here reaches disk. Set `client_identity_headers:` in ensemble.yaml if
// your stack already spells this header differently.
const CLIENT_ID = 'web';

async function request(path, opts = {}) {
  const res = await fetch(`${EDGE_URL}${path}`, {
    ...opts,
    headers: {
      Authorization: AUTH_HEADER,
      'x-source-client': CLIENT_ID,
      ...(opts.body ? { 'content-type': 'application/json' } : {}),
      ...opts.headers,
    },
  });

  // A service behind a live proxy can still be down — the proxy answers a
  // plain-text 502 ("dial tcp ...: connection refused") rather than
  // refusing the connection outright, so a non-JSON body means the same
  // thing as a network failure. See sample/README.md.
  const isJSON = (res.headers.get('content-type') || '').includes('application/json');
  const body = isJSON ? await res.json() : await res.text();

  if (!res.ok) {
    const message = isJSON && body && body.error ? body.error : String(body).slice(0, 200);
    throw new Error(message || `${path} failed (${res.status})`);
  }
  return body;
}

export const listProducts = () => request('/products');

export const getCart = (userId) => request(`/cart/${userId}`);

export const addToCart = (userId, productId, quantity) =>
  request(`/cart/${userId}/items`, {
    method: 'POST',
    body: JSON.stringify({ product_id: productId, quantity }),
  });

export const removeFromCart = (userId, productId) =>
  request(`/cart/${userId}/items/${productId}`, { method: 'DELETE' });

export const checkout = (userId) =>
  request(`/cart/${userId}/checkout`, { method: 'POST' });
