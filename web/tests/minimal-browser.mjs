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
    await expect(page.getByText('Workflows: Disabled', { exact: true })).toBeVisible();
    await expect(page.getByText('Events: Disabled', { exact: true })).toBeVisible();
    await expect(page.getByText('OK', { exact: true })).toHaveCount(3);
  }
  await page.goto(new URL('wiring?binding=hello.Greet&view=test', base).href);
  await expect(page.getByText('Execution is disabled in this deployment', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Test', exact: true })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Runs', exact: true })).toBeDisabled();
  await page.getByRole('button', { name: 'YAML', exact: true }).click();
  await expect(page.getByRole('textbox', { name: 'Definition', exact: true })).toBeVisible();
  assert.deepEqual(errors, []);
  process.stdout.write(
    'Minimal Compose: login, assets, both themes, infrastructure and disabled execution with editable definitions passed\n',
  );
} finally {
  await browser.close();
}
