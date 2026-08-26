// Drives the same money path as sample/README.md's curl walkthrough,
// through the actual browser. Needs the full sample stack running
// (`ensemble up -c ../../ensemble.yaml`) and reachable at the ensemble
// API's default 127.0.0.1:4700 — see playwright.config.js.
//
// `test` comes from @caribou-crew/retrace-playwright rather than
// @playwright/test: it is the same test function with one extra fixture,
// `retrace`, whose checkpoint/group calls are no-ops when nothing is
// recording. So `pnpm run e2e` behaves exactly as it did before, and
// `retrace run` gets screenshots and flow parts out of the same file
// instead of a second copy of it. `expect` is unchanged and still comes
// straight from @playwright/test.
import { expect } from '@playwright/test';
import { test } from '@caribou-crew/retrace-playwright';

const ENSEMBLE_API = process.env.ENSEMBLE_API || 'http://127.0.0.1:4700';

test.beforeEach(async ({ request }) => {
  // baseline is idempotent — re-seeding resets products/users to a known
  // state before each test, independent of what a prior run left behind.
  // This talks to ensemble's control plane directly, not through the app's
  // edge, so the seed's own work never lands in a recording as though the
  // app had made those calls.
  await request.post(`${ENSEMBLE_API}/api/seed/baseline`);
});

test('browse, add to cart, and check out', async ({ page, retrace }) => {
  await page.goto('/');

  await retrace.group('browse');
  await expect(page.locator('.products li').first()).toBeVisible();
  await expect(page.locator('header input')).toHaveValue('1');
  await retrace.checkpoint('catalog');

  const firstProduct = page.locator('.products li').first();
  const name = await firstProduct.locator('span').first().innerText();
  await firstProduct.getByRole('button', { name: 'add to cart' }).click();

  await expect(page.locator('.cart li').first()).toContainText(name);
  await expect(page.locator('.total')).toContainText(/\$\d+\.\d{2}/);
  await retrace.checkpoint('cart');
  await retrace.endGroup();

  await retrace.group('checkout');
  await page.getByRole('button', { name: 'checkout' }).click();

  const confirmation = page.locator('section', { hasText: 'order confirmed' });
  await expect(confirmation).toContainText(/order #\d+ — paid/);
  // "empty" renders as a sibling <p>, not inside the <ul class="cart"> —
  // scope to the cart section, not the list itself.
  await expect(page.locator('section', { hasText: 'cart' }).locator('p')).toContainText('empty');
  // The confirmation carries a per-order id, so the full-page shot would
  // differ on every run for a reason that is not a regression. Scope the
  // checkpoint to the cart section instead: it is the part of this screen
  // whose emptying is the assertion, and it holds no order number.
  await retrace.checkpoint('cart-emptied', { selector: 'section:has(.cart)' });
  await retrace.endGroup();
});

test('checking out for an unseeded user id surfaces the backend error', async ({ page, retrace }) => {
  // 1 and 2 are the only ids baseline seeds (seeds/users.sql) — order
  // validates against user-svc and rejects anything else. storefront-bff's
  // cart itself has no such check, so add-to-cart succeeds regardless;
  // this is specifically exercising order's validation, not the cart.
  await page.goto('/');
  await retrace.group('unknown-user');
  await page.locator('header input').fill('999');

  await page.locator('.products li').first().getByRole('button', { name: 'add to cart' }).click();
  await expect(page.locator('.cart li').first()).toBeVisible();

  await page.getByRole('button', { name: 'checkout' }).click();
  await expect(page.locator('.error')).toContainText('unknown user');
  await retrace.checkpoint('unknown-user-error');

  // Empty this cart before leaving. `beforeEach` reseeds Postgres, which is
  // where products and users live — carts live in DynamoDB and it does not
  // touch them. User 1's cart self-heals because checkout empties it, but
  // 999 never checks out successfully, so without this its cart grows by
  // one item per run and every run's recording differs from the last for a
  // reason that is not a regression. Found by diffing two consecutive runs.
  await page.getByRole('button', { name: 'remove' }).click();
  await expect(page.locator('section', { hasText: 'cart' }).locator('p')).toContainText('empty');
  await retrace.endGroup();
});
