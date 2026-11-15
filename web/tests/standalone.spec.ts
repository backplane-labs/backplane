import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';

const origin = process.env.MODULE_TEST_URL ?? 'http://127.0.0.1:4179';

test('module runs without a platform, owns multiple relative pages and supports deep links', async ({ page }) => {
  const failures: string[] = [];
  page.on('pageerror', (error) => failures.push(error.message));
  page.on('request', (request) => {
    if (!request.url().startsWith(`${origin}/`)) failures.push(`External request: ${request.url()}`);
  });
  await page.goto(`${origin}/`);
  await expect(page.getByRole('heading', { name: 'Hello module' })).toBeVisible();
  await expect(page.getByRole('cell', { name: 'formatter', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Open settings' }).click();
  await expect(page).toHaveURL(`${origin}/settings`);
  await page.reload();
  await page.getByLabel('Display name').fill('My module');
  await page.getByRole('button', { name: 'Save locally' }).click();
  await expect(page.getByRole('status')).toHaveText('Saved: My module');
  await page.getByRole('button', { name: 'Light theme' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.getByRole('button', { name: 'Back to overview' }).click();
  expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations).toEqual([]);
  await page.goto(`${origin}/nested/missing`);
  await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible();
  expect(failures).toEqual([]);
});

test('advanced kit packages share providers and load lazy assets', async ({ page }) => {
  const failures: string[] = [];
  page.on('pageerror', (error) => failures.push(error.message));
  await page.goto(`${origin}/workbench`);
  await expect(page.getByRole('heading', { name: 'Kit workbench' })).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Trace query' })).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Sequence', exact: true })).toHaveValue('18446744073709551615');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('output')).toContainText('18446744073709551615');
  await expect(page.getByText('Local fixture request completed', { exact: true })).toBeVisible();
  await expect(page.locator('canvas').first()).toBeVisible();
  await page.getByRole('button', { name: 'Light theme' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Kit workbench' })).toBeVisible();
  expect(failures).toEqual([]);
});
