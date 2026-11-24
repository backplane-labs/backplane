import assert from 'node:assert/strict';
import { chromium, expect } from '@playwright/test';

const base = process.env.BACKPLANE_MINIMAL_URL ?? 'http://127.0.0.1:8081/backplane/';
const token = process.env.DEV_ADMIN_TOKEN ?? 'dev-admin-token-change-me';
const browser = await chromium.launch();
try {
  const page = await browser.newPage();
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  // Compose --wait does not prove application readiness; retry the actual login page.
  await expect(async () => {
    await page.goto(base);
    await expect(page.getByLabel('Operator token', { exact: true })).toBeVisible({ timeout: 1000 });
  }).toPass({ timeout: 60000, intervals: [1000] });
  await page.getByLabel('Operator token', { exact: true }).fill(token);
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await page
    .getByRole('navigation', { name: 'Platform', exact: true })
    .getByRole('link', { name: 'Infrastructure', exact: true })
    .click();
  for (const theme of ['Light theme', 'Dark theme']) {
    await page.getByRole('button', { name: theme, exact: true }).click();
    await expect(page.getByText(/Workflows:/)).toHaveCount(0);
    await expect(page.getByText(/Events:/)).toHaveCount(0);
    const table = page.getByRole('table', { name: 'Dependencies', exact: true });
    await expect(table.locator('tbody tr')).toHaveCount(2);
    for (const name of ['PostgreSQL', 'Consul']) {
      await expect(table.getByRole('row').filter({ hasText: name }).getByText('OK', { exact: true })).toBeVisible();
    }
  }
  await page.goto(new URL('wiring?binding=hello.Greet&view=test', base).href);
  await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible();
  assert.deepEqual(errors, []);
  process.stdout.write(
    'Minimal Compose: login, assets, both themes, infrastructure and retired operator routes passed\n',
  );
} finally {
  await browser.close();
}
