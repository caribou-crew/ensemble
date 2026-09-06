import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createServer } from 'node:http';
import { resetFixtures } from './reset-fixtures.mjs';

async function fixture(t, { seedStatus = 200, seed = { ok: true, results: [{ ok: true }] }, deleteStatus = 200, persist = true } = {}) {
  const carts = new Map([['1', [{ product_id: 2, quantity: 3 }, { product_id: 4, quantity: 1 }]], ['999', [{ product_id: 2, quantity: 7 }]]]);
  const events = [];
  const server = createServer((req, res) => {
    events.push(`${req.method} ${req.url}`);
    res.setHeader('content-type', 'application/json');
    if (req.url === '/api/seed/baseline') {
      res.writeHead(seedStatus).end(JSON.stringify(seed));
      return;
    }
    const match = req.url.match(/^\/cart\/(1|999)(?:\/items\/(\d+))?$/);
    if (!match || req.headers.authorization !== 'Bearer demo-token') { res.writeHead(400).end('{}'); return; }
    const [, user, product] = match;
    if (req.method === 'DELETE') {
      if (deleteStatus === 200 && persist) carts.set(user, carts.get(user).filter((item) => item.product_id !== Number(product)));
      res.writeHead(deleteStatus).end(JSON.stringify({ user_id: user, items: carts.get(user) }));
      return;
    }
    res.end(JSON.stringify({ user_id: user, items: carts.get(user) }));
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const url = `http://127.0.0.1:${server.address().port}`;
  return { apiURL: url, edgeURL: url, carts, events };
}

test('repeated setup removes leftovers for both test users, including multiple products', async (t) => {
  const state = await fixture(t);
  await resetFixtures(state);
  assert.deepEqual([...state.carts.values()], [[], []]);
  state.carts.get('999').push({ product_id: 8, quantity: 5 });
  await resetFixtures(state);
  assert.deepEqual([...state.carts.values()], [[], []]);
});

for (const options of [{ seedStatus: 500 }, { seed: { ok: false, results: [{ ok: true }] } }, { seed: {} }, { seed: { ok: true, results: [] } }, { seed: { ok: true, results: [{ ok: false }] } }]) {
  test('failed or unassessed seed refuses before cart mutation: ' + JSON.stringify(options), async (t) => {
    const state = await fixture(t, options);
    await assert.rejects(resetFixtures(state), /seed/i);
    assert.deepEqual(state.events, ['POST /api/seed/baseline']);
  });
}

test('a failed cart deletion refuses setup', async (t) => {
  const state = await fixture(t, { deleteStatus: 500 });
  await assert.rejects(resetFixtures(state), /cart.*500/i);
});

test('a successful deletion response does not excuse a cart that is still populated', async (t) => {
  const state = await fixture(t, { persist: false });
  await assert.rejects(resetFixtures(state), /cart.*empty/i);
});
