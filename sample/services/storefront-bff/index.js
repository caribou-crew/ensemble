// storefront-bff is the "brew" sample stack's backend-for-frontend: it owns
// the shopping cart (backed by DynamoDB Local) and checks out by calling
// order-svc. It knows nothing about ensemble beyond two ordinary HTTP
// headers — see forwardTraceHeaders.
'use strict';

const express = require('express');
const { DynamoDBClient, CreateTableCommand } = require('@aws-sdk/client-dynamodb');
const {
  DynamoDBDocumentClient,
  GetCommand,
  PutCommand,
} = require('@aws-sdk/lib-dynamodb');

const PORT = process.env.PORT || '8085';
const CATALOG_URL = process.env.CATALOG_URL;
const ORDER_URL = process.env.ORDER_URL;
const DYNAMODB_URL = process.env.DYNAMODB_URL;
const DYNAMODB_TABLE = process.env.DYNAMODB_TABLE || 'carts';

// forwardTraceHeaders is the whole propagation contract: carry the W3C
// traceparent/baggage headers ensemble's proxy stamped on the inbound
// request onto any outbound request this service makes on its own behalf,
// so the hop chain in the dashboard doesn't break. No ensemble dependency
// required — just two headers.
function forwardTraceHeaders(dst, src) {
  const traceparent = src.get('traceparent');
  if (traceparent) dst.set('traceparent', traceparent);
  const baggage = src.get('baggage');
  if (baggage) dst.set('baggage', baggage);
}

// ensureTable creates the carts table if it doesn't already exist, and
// retries connecting for up to ~30s in case DynamoDB Local isn't ready yet
// (it can take a few seconds to come up after `docker run` returns).
async function ensureTable(client, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  let lastErr;
  while (Date.now() < deadline) {
    try {
      await client.send(
        new CreateTableCommand({
          TableName: DYNAMODB_TABLE,
          AttributeDefinitions: [{ AttributeName: 'userId', AttributeType: 'S' }],
          KeySchema: [{ AttributeName: 'userId', KeyType: 'HASH' }],
          BillingMode: 'PAY_PER_REQUEST',
        })
      );
      return;
    } catch (err) {
      if (err.name === 'ResourceInUseException') {
        // Table already exists — fine.
        return;
      }
      lastErr = err;
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
  }
  throw new Error(`timed out waiting for DynamoDB Local: ${lastErr}`);
}

async function getCart(docClient, userId) {
  const result = await docClient.send(
    new GetCommand({ TableName: DYNAMODB_TABLE, Key: { userId: String(userId) } })
  );
  return result.Item ? result.Item.items : [];
}

async function putCart(docClient, userId, items) {
  await docClient.send(
    new PutCommand({
      TableName: DYNAMODB_TABLE,
      Item: { userId: String(userId), items },
    })
  );
}

function buildApp(docClient) {
  const app = express();
  app.use(express.json());

  app.get('/healthz', (req, res) => {
    res.sendStatus(200);
  });

  app.get('/cart/:userId', async (req, res) => {
    try {
      const items = await getCart(docClient, req.params.userId);
      res.status(200).json({ user_id: req.params.userId, items });
    } catch (err) {
      res.status(500).json({ error: err.message });
    }
  });

  app.post('/cart/:userId/items', async (req, res) => {
    const { userId } = req.params;
    const productId = req.body && req.body.product_id;
    const quantity = req.body && req.body.quantity;
    if (productId === undefined || quantity === undefined) {
      res.status(400).json({ error: 'product_id and quantity are required' });
      return;
    }

    try {
      const catalogHeaders = new Headers();
      forwardTraceHeaders(catalogHeaders, new Headers(req.headers));

      let catalogResp;
      try {
        catalogResp = await fetch(`${CATALOG_URL}/products/${productId}`, {
          headers: catalogHeaders,
        });
      } catch (err) {
        res.status(502).json({ error: 'catalog service unavailable' });
        return;
      }

      if (catalogResp.status === 404) {
        res.status(404).json({ error: 'unknown product' });
        return;
      }
      if (!catalogResp.ok) {
        res.status(502).json({ error: 'catalog service error' });
        return;
      }

      const items = await getCart(docClient, userId);
      const existing = items.find((item) => item.product_id === productId);
      if (existing) {
        existing.quantity += quantity;
      } else {
        items.push({ product_id: productId, quantity });
      }

      await putCart(docClient, userId, items);
      res.status(200).json({ user_id: userId, items });
    } catch (err) {
      res.status(500).json({ error: err.message });
    }
  });

  app.delete('/cart/:userId/items/:productId', async (req, res) => {
    const { userId } = req.params;
    const productId = Number(req.params.productId);

    try {
      const items = await getCart(docClient, userId);
      const filtered = items.filter((item) => item.product_id !== productId);
      await putCart(docClient, userId, filtered);
      res.status(200).json({ user_id: userId, items: filtered });
    } catch (err) {
      res.status(500).json({ error: err.message });
    }
  });

  app.post('/cart/:userId/checkout', async (req, res) => {
    const { userId } = req.params;

    try {
      const items = await getCart(docClient, userId);
      if (items.length === 0) {
        res.status(400).json({ error: 'cart is empty' });
        return;
      }

      const orderHeaders = new Headers({ 'content-type': 'application/json' });
      forwardTraceHeaders(orderHeaders, new Headers(req.headers));

      const body = JSON.stringify({
        user_id: parseInt(userId, 10),
        items: items.map((item) => ({
          product_id: item.product_id,
          quantity: item.quantity,
        })),
      });

      let orderResp;
      try {
        orderResp = await fetch(`${ORDER_URL}/orders`, {
          method: 'POST',
          headers: orderHeaders,
          body,
        });
      } catch (err) {
        res.status(503).json({ error: 'ordering unavailable' });
        return;
      }

      // order-svc runs behind the "full" profile and is often not running.
      // A dead upstream doesn't necessarily fail the fetch() itself —
      // ensemble's proxy keeps listening even when the real process behind
      // it is down, and answers with a 502 plain-text body ("dial tcp
      // ...: connection refused") rather than a JSON response. Treat any
      // non-JSON body the same as a connection failure.
      if (!(orderResp.headers.get('content-type') || '').includes('application/json')) {
        res.status(503).json({ error: 'ordering unavailable' });
        return;
      }
      const orderBody = await orderResp.json();

      if (orderResp.status >= 200 && orderResp.status < 300) {
        await putCart(docClient, userId, []);
      }

      res.status(orderResp.status).json(orderBody);
    } catch (err) {
      res.status(500).json({ error: err.message });
    }
  });

  return app;
}

async function main() {
  const client = new DynamoDBClient({
    endpoint: DYNAMODB_URL,
    region: 'us-east-1',
    credentials: { accessKeyId: 'local', secretAccessKey: 'local' },
  });
  const docClient = DynamoDBDocumentClient.from(client);

  await ensureTable(client);

  const app = buildApp(docClient);
  app.listen(PORT, () => {
    console.log(
      `storefront-bff listening on :${PORT} (catalog=${CATALOG_URL} order=${ORDER_URL} dynamodb=${DYNAMODB_URL} table=${DYNAMODB_TABLE})`
    );
  });
}

main().catch((err) => {
  console.error('storefront-bff failed to start:', err);
  process.exit(1);
});
