// Drives the same money path as sample/README.md's curl walkthrough,
// through the actual browser. Needs the full sample stack running
// (`--profile full`, for order-svc) and reachable at the ensemble API's
// default 127.0.0.1:4700 — see playwright.config.js.
import { test, expect } from '@playwright/test';

const ENSEMBLE_API = process.env.ENSEMBLE_API || 'http://127.0.0.1:4700';

test.beforeEach(async ({ request }) => {
  // baseline is idempotent — re-seeding resets products/users to a known
  // state before each test, independent of what a prior run left behind.
  await request.post(`${ENSEMBLE_API}/api/seed/baseline`);
});

test('browse, add to cart, and check out', async ({ page }) => {
  await page.goto('/');

  await expect(page.locator('.products li').first()).toBeVisible();
  await expect(page.locator('header input')).toHaveValue('1');

  const firstProduct = page.locator('.products li').first();
  const name = await firstProduct.locator('span').first().innerText();
  await firstProduct.getByRole('button', { name: 'add to cart' }).click();

  await expect(page.locator('.cart li').first()).toContainText(name);
  await expect(page.locator('.total')).toContainText(/\$\d+\.\d{2}/);

  await page.getByRole('button', { name: 'checkout' }).click();

  const confirmation = page.locator('section', { hasText: 'order confirmed' });
  await expect(confirmation).toContainText(/order #\d+ — paid/);
  // "empty" renders as a sibling <p>, not inside the <ul class="cart"> —
  // scope to the cart section, not the list itself.
  await expect(page.locator('section', { hasText: 'cart' }).locator('p')).toContainText('empty');
});

test('checking out for an unseeded user id surfaces the backend error', async ({ page }) => {
  // 1 and 2 are the only ids baseline seeds (seeds/users.sql) — order-svc
  // validates against user-svc and rejects anything else. storefront-bff's
  // cart itself has no such check, so add-to-cart succeeds regardless;
  // this is specifically exercising order-svc's validation, not the cart.
  await page.goto('/');
  await page.locator('header input').fill('999');

  await page.locator('.products li').first().getByRole('button', { name: 'add to cart' }).click();
  await expect(page.locator('.cart li').first()).toBeVisible();

  await page.getByRole('button', { name: 'checkout' }).click();
  await expect(page.locator('.error')).toContainText('unknown user');
});
