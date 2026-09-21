import { test, expect, type Page } from '@playwright/test';
import { installFixtures } from './fixtures';

const requests = new WeakMap<Page, string[]>();
const errors = new WeakMap<Page, string[]>();
test.afterEach(async ({ page }) => { expect(requests.get(page)).toEqual([]); expect(errors.get(page)).toEqual([]); });

test.beforeEach(async ({ page }) => {
  requests.set(page, await installFixtures(page));
  errors.set(page, []);
  page.on('pageerror', error => { errors.get(page)!.push(error.message); });
  // A build is selected automatically: opening the suites view is the only click.
  await page.goto('/?view=suites');
  await expect(page.getByRole('region', { name: 'Build gallery' })).toBeVisible();
});

test('opens on the gallery with reference, candidate and diff images that actually load', async ({ page }) => {
  await expect(page.getByRole('button', { name: 'Gallery', exact: true })).toHaveAttribute('aria-pressed', 'true');
  const images = page.locator('.gallery__table tbody img');
  await expect(images).toHaveCount(6);
  await expect.poll(() => images.evaluateAll(imgs => imgs.every(i => (i as HTMLImageElement).naturalWidth === 320))).toBe(true);
  await expect(images.first()).toHaveAttribute('src', /pinned-run\/pinned-reference\/shots\/a\/sign-in$/);
  await expect(page.locator('.gallery__lanes')).toContainText('aaaaaaa');
  await expect(page.locator('.gallery__summary')).toContainText('3 flows');
  // A lane with no result is a dash, never an image or a pass.
  await expect(page.locator('.gallery__table tbody tr').filter({ hasText: 'Sign in' }).locator('.gallery__empty').first()).toBeVisible();
});

test('shows a network chip per comparison and ranks the serious ones on the Network tab', async ({ page }) => {
  await expect(page.locator('.gallery__chip').first()).toHaveText('1 changed · 1 missing');
  await page.getByRole('button', { name: /^Network/ }).click();
  const rows = page.locator('.gallery__table--net tbody tr');
  await expect(rows).toHaveCount(2);
  await expect(rows.first()).toContainText('Sign in');
  await expect(page.locator('.gallery__network')).toContainText('1 with missing or extra requests');
  await expect(page.locator('.gallery__nowire')).toContainText('Missing report evidence');
});

test('filters and the view switch keep working, and flow review is one click away', async ({ page }) => {
  await page.getByLabel('Gallery filter').selectOption('network');
  await expect(page.locator('.gallery__flow')).toHaveCount(2);
  await page.getByLabel('Search gallery flows').fill('profile');
  await expect(page.locator('.gallery__flow')).toHaveCount(1);
  await page.getByRole('button', { name: 'Flow review', exact: true }).click();
  await expect(page.getByRole('region', { name: 'Review workspace' })).toBeVisible();
  await expect(page).toHaveURL(/suiteView=review/);
  await page.reload();
  await expect(page.getByRole('region', { name: 'Review workspace' })).toBeVisible();
});

test('narrow layout keeps the gallery reachable', async ({ page }) => {
  await page.setViewportSize({ width: 600, height: 900 });
  await expect(page.locator('.gallery__bar')).toBeVisible();
  await expect(page.getByRole('button', { name: /^Network/ })).toBeVisible();
});
