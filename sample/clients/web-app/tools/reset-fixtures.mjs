// Shared Playwright/Maestro setup. These requests use the normal sample edge,
// never RETRACE_PROXY_URL, so fixture maintenance is outside the test session.
export async function resetFixtures({
  apiURL = process.env.ENSEMBLE_API || 'http://127.0.0.1:4700',
  edgeURL = process.env.BREW_SETUP_EDGE_URL || 'http://127.0.0.1:9080',
  timeoutMs = 10000,
} = {}) {
  async function request(base, path, method, label, headers = {}) {
    const response = await fetch(base.replace(/\/+$/, '') + path, {
      method, headers, redirect: 'error', signal: AbortSignal.timeout(timeoutMs),
    });
    if (!response.ok) throw new Error(`${label} returned HTTP ${response.status}`);
    try { return await response.json(); }
    catch { throw new Error(`${label} returned invalid JSON`); }
  }

  const seed = await request(apiURL, '/api/seed/baseline', 'POST', 'baseline seed');
  if (seed?.ok !== true || !Array.isArray(seed.results) || seed.results.length === 0 || seed.results.some((step) => step?.ok !== true)) {
    throw new Error('baseline seed did not confirm that every step succeeded');
  }

  const headers = { authorization: 'Bearer demo-token' };
  for (const user of ['1', '999']) {
    const path = `/cart/${user}`;
    const readCart = async () => {
      const cart = await request(edgeURL, path, 'GET', `cart ${user}`, headers);
      if (cart?.user_id !== user || !Array.isArray(cart.items) || cart.items.some((item) => !Number.isSafeInteger(item?.product_id) || item.product_id <= 0)) {
        throw new Error(`cart ${user} returned invalid fixture data`);
      }
      return cart.items;
    };
    for (const item of await readCart()) {
      await request(edgeURL, `${path}/items/${item.product_id}`, 'DELETE', `cart ${user} delete`, headers);
    }
    if ((await readCart()).length !== 0) throw new Error(`cart ${user} is not empty after setup`);
  }
}
