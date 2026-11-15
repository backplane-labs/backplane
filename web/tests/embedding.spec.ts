import { expect, test } from '@playwright/test';

const base = 'http://127.0.0.1:4180/backplane';
test('remote uses host providers under a prefix, survives deep links and obeys console CSP', async ({ page }) => {
  const failures: string[] = [];
  const remoteRequests: string[] = [];
  page.on('pageerror', (error) => failures.push(error.message));
  page.on('requestfailed', (request) => failures.push(`${request.url()}: ${request.failure()?.errorText}`));
  page.on('request', (request) => {
    if (request.url().includes('/plugins/')) remoteRequests.push(request.url());
    if (!request.url().startsWith(base + '/')) failures.push(`Request outside console prefix: ${request.url()}`);
  });
  const response = await page.goto(`${base}/s/hello/`);
  expect(response?.headers()['content-security-policy']).toContain("script-src 'self';");
  await expect(page.getByRole('heading', { name: 'Hello module' })).toBeVisible();
  await expect(page.getByText('Embedded in Backplane', { exact: true })).toBeVisible();
  await expect(page.getByRole('cell', { name: 'formatter', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Open settings' }).click();
  await expect(page).toHaveURL(`${base}/s/hello/settings`);
  await page.reload();
  await page.getByLabel('Display name').fill('Embedded module');
  await page.getByRole('button', { name: 'Save locally' }).click();
  await expect(page.getByRole('status')).toHaveText('Saved: Embedded module');
  await page.getByRole('button', { name: 'Light theme' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.getByRole('link', { name: 'Platform home' }).click();
  await expect(page.getByRole('heading', { name: 'Embedding fixture' })).toBeVisible();
  await page.getByRole('link', { name: 'Settings', exact: true }).click();
  await expect(page.getByLabel('Display name')).toHaveValue('Hello');
  expect(remoteRequests.some((url) => url.endsWith('/remoteEntry.js'))).toBe(true);
  expect(failures).toEqual([]);
});

test('incompatible SDK metadata is rejected before remote JavaScript loads', async ({ page }) => {
  const scripts: string[] = [];
  page.on('request', (request) => {
    if (request.url().includes('/plugins/') && request.resourceType() === 'script') scripts.push(request.url());
  });
  await page.route('**/plugins/hello/*/plugin.json', (route) =>
    route.fulfill({ json: { name: 'hello', sdk_major: 999, nav: [] } }),
  );
  await page.goto(`${base}/s/hello/`);
  await expect(page.getByRole('alert')).toContainText('Plugin metadata does not match the catalog');
  expect(scripts).toEqual([]);
});

test('advanced kit packages share providers and load lazy assets', async ({ page }) => {
  const failures: string[] = [];
  page.on('pageerror', (error) => failures.push(error.message));
  await page.goto(`${base}/s/hello/workbench`);
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
