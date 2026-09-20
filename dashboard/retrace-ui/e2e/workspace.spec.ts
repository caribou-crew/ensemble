import { test, expect, type Page } from '@playwright/test';
import { installFixtures } from './fixtures';

const requests = new WeakMap<Page, string[]>();
const errors = new WeakMap<Page, string[]>();
test.afterEach(async ({ page }) => { expect(requests.get(page)).toEqual([]); expect(errors.get(page)).toEqual([]); });

test.beforeEach(async ({ page }) => {
  const unexpected = await installFixtures(page, { brokenImage: test.info().title.includes('image request fails') });
  requests.set(page, unexpected);
  errors.set(page, []);
  page.on('pageerror', error => { errors.get(page)!.push(error.message); });
  await page.goto('/?view=suites');
  await expect(page.getByRole('region', { name: 'Review workspace' })).toBeVisible();
  expect(unexpected).toEqual([]);
});

test('originals load immediately, and next flow, diff and wire stay in the workspace', async ({ page }) => {
  const images = page.locator('.suite-evidence img');
  await expect(images).toHaveCount(2);
  await expect.poll(() => images.evaluateAll(imgs => imgs.every(i => (i as HTMLImageElement).naturalWidth === 320))).toBe(true);
  await expect(images.first()).toHaveAttribute('src', /pinned-run\/pinned-reference\/shots\/a\/sign-in$/);
  await expect(images.nth(1)).toHaveAttribute('src', /pinned-run\/pinned-reference\/shots\/b\/sign-in$/);
  await page.getByRole('button', { name: 'Next flow →', exact: true }).click();
  await expect(page.locator('.suite-review__flow-heading h2')).toHaveText('Edit profile');
  await page.getByRole('button', { name: 'Diff', exact: true }).click();
  await expect(images).toHaveCount(1);
  await expect(images).toHaveAttribute('src', /\/diff\/profile$/);
  await page.getByRole('button', { name: 'Wire traffic', exact: true }).click();
  await expect(page.getByText('/example/profile', { exact: true }).first()).toBeVisible();
  await expect(page.getByRole('complementary', { name: 'Flow queue' })).toBeVisible();
  await expect(page).toHaveURL(/view=suites/);
});

test('passing selection, filters and search survive full comparison and reload', async ({ page }) => {
  await page.getByLabel('Result filter', { exact: true }).selectOption('pass');
  await page.getByLabel('Search flows', { exact: true }).fill('sign out');
  await page.locator('.suite-review__row').click();
  await page.getByRole('button', { name: 'Full comparison ↗', exact: true }).click();
  await page.getByRole('button', { name: /back to comparison suites/ }).click();
  await page.reload();
  await expect(page.getByLabel('Result filter', { exact: true })).toHaveValue('pass');
  await expect(page.getByLabel('Search flows', { exact: true })).toHaveValue('sign out');
  await expect(page.locator('.suite-review__flow-heading h2')).toHaveText('Sign out');
  await expect(page.locator('.suite-review__row')).toHaveCount(1);
});

test('keyboard focus follows selection while typing remains independent', async ({ page }) => {
  const first = page.locator('.suite-review__row').first();
  await first.focus();
  await first.press('j');
  const selected = page.locator('.suite-review__row[aria-pressed=true]');
  await expect(selected).toContainText('Edit profile');
  await expect(selected).toBeFocused();
  await selected.press('k');
  await expect(page.locator('.suite-review__flow-heading h2')).toHaveText('Sign in');
  const selectedFlow = new URL(page.url()).searchParams.get('suiteFlow');
  await page.getByLabel('Search flows').press('j');
  expect(new URL(page.url()).searchParams.get('suiteFlow')).toBe(selectedFlow);
  await expect(page.getByLabel('Search flows')).toHaveValue('j');
  await expect(page.getByText('No flows match.', { exact: false })).toBeVisible();
});

test('missing evidence and missing screenshots never look like passing comparisons', async ({ page }) => {
  await page.getByLabel('Search flows').fill('missing report');
  await expect(page.getByText('No linked comparison evidence.', { exact: false })).toBeVisible();
  await expect(page.locator('.suite-evidence img')).toHaveCount(0);
  await page.getByLabel('Search flows').fill('no recorded');
  await expect(page.getByText('No screenshots were recorded', { exact: false })).toBeVisible();
  await page.getByLabel('Search flows').fill('scheduled');
  await expect(page.getByText('No runner result.', { exact: false }).first()).toBeVisible();
});

test('image request fails visibly and next flow recovers', async ({ page }) => {
  await expect(page.getByRole('alert').filter({ hasText: 'Unable to load' })).toBeVisible();
  await expect(page.locator('.suite-evidence img')).toHaveCount(1);
  await page.getByRole('button', { name: 'Next flow →', exact: true }).click();
  await expect(page.locator('.suite-evidence img')).toHaveCount(2);
  await expect.poll(() => page.locator('.suite-evidence img').evaluateAll(imgs => imgs.every(i => (i as HTMLImageElement).naturalWidth === 320))).toBe(true);
  await expect(page.getByRole('alert')).toHaveCount(0);
});

test('narrow layout keeps controls reachable without horizontal overflow', async ({ page }) => {
  await page.setViewportSize({ width: 600, height: 900 });
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.getByLabel('Feature filter').selectOption('account');
  await expect(page.locator('.suite-review__row')).toHaveCount(4);
  await page.getByRole('button', { name: 'Next flow →', exact: true }).click();
  await expect(page.locator('.suite-review__flow-heading h2')).toHaveText('Missing report evidence');
});
