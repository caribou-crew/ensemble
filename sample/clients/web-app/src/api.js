// api.js talks to edge-gw — the sample stack's entry point — and nothing
// else. edge-gw's proxy stamps a fresh traceparent on any request that
// doesn't already carry one, so a plain browser fetch() is a valid trace
// root; this file has no tracing code of its own.
const EDGE_URL = import.meta.env.VITE_EDGE_URL || 'http://127.0.0.1:9080';
const AUTH_HEADER = 'Bearer demo-token';

async function request(path, opts = {}) {
  const res = await fetch(`${EDGE_URL}${path}`, {
    ...opts,
    headers: {
      Authorization: AUTH_HEADER,
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
