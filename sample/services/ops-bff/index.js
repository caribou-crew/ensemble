// ops-bff is the "brew" sample stack's internal/admin read-only backend: it
// aggregates data from catalog-svc and order-svc for ops tooling. It knows
// nothing about ensemble beyond two ordinary HTTP headers — see
// forwardTraceHeaders.
'use strict';

const express = require('express');

const PORT = process.env.PORT || 8086;
const CATALOG_URL = process.env.CATALOG_URL || 'http://127.0.0.1:9081';
const ORDER_URL = process.env.ORDER_URL || 'http://127.0.0.1:9083';

const app = express();

app.get('/healthz', (req, res) => {
  res.sendStatus(200);
});

app.get('/admin/products', async (req, res) => {
  const headers = {};
  forwardTraceHeaders(headers, req.headers);

  let upstream;
  try {
    upstream = await fetch(`${CATALOG_URL}/products`, { headers });
  } catch (err) {
    res.status(502).json({ error: 'products unavailable' });
    return;
  }

  // See the same check in /admin/orders: ensemble's proxy answers with a
  // non-JSON 502 body when the real service behind it is down, even though
  // the fetch() itself succeeds.
  if (!(upstream.headers.get('content-type') || '').includes('application/json')) {
    res.status(502).json({ error: 'products unavailable' });
    return;
  }

  const body = await upstream.json();
  res.status(upstream.status).json(body);
});

app.get('/admin/orders', async (req, res) => {
  const headers = {};
  forwardTraceHeaders(headers, req.headers);

  let upstream;
  try {
    upstream = await fetch(`${ORDER_URL}/orders`, { headers });
  } catch (err) {
    // `order` is a variants: service and its `real` (Java/MySQL) backing
    // is opt-in, so it can be mid-swap or not running at all — a
    // connection failure here is expected, not a crash.
    res.status(503).json({ error: 'orders unavailable' });
    return;
  }

  // ensemble's proxy keeps listening even when the real process behind it
  // is down, and answers with a 502 plain-text body ("dial tcp ...:
  // connection refused") rather than JSON — so fetch() doesn't always
  // throw just because order-svc itself is unreachable. Treat a non-JSON
  // response from what's supposed to be a JSON API the same way.
  if (!(upstream.headers.get('content-type') || '').includes('application/json')) {
    res.status(503).json({ error: 'orders unavailable' });
    return;
  }

  const body = await upstream.json();
  res.status(upstream.status).json(body);
});

app.listen(PORT, () => {
  console.log(`ops-bff listening on :${PORT} (catalog=${CATALOG_URL}, orders=${ORDER_URL})`);
});

// forwardTraceHeaders is the whole propagation contract: carry the W3C
// traceparent/baggage headers ensemble's proxy stamped on the inbound
// request onto any outbound request this service makes on its own behalf,
// so the hop chain in the dashboard doesn't break. No ensemble dependency
// required — just two headers.
function forwardTraceHeaders(dst, src) {
  const tp = src['traceparent'];
  if (tp) {
    dst['traceparent'] = tp;
  }
  const bg = src['baggage'];
  if (bg) {
    dst['baggage'] = bg;
  }
}
