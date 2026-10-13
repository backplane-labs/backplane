import assert from 'node:assert/strict';
import { chromium, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

const origin = process.env.BACKPLANE_DEV_URL ?? 'http://127.0.0.1:8081';
const browser = await chromium.launch();
try {
  const context = await browser.newContext({ viewport: { width: 1440, height: 960 } });
  let dropUpdates = false, disconnected = false, clientSocket;
  // Keep the socket connected while withholding replies: an open socket alone
  // must not keep old dependency checks green.
  await context.routeWebSocket('**/backplane/ws', (route) => {
    if (disconnected) { route.close({ code: 1012, reason: 'acceptance disconnect' }); return; }
    clientSocket = route;
    const serverSocket = route.connectToServer();
    serverSocket.onMessage((message) => { if (!dropUpdates) route.send(message); });
  });
  const page = await context.newPage(), errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.goto(`${origin}/backplane/infrastructure`);
  await page.getByLabel('Operator token').fill(process.env.DEV_ADMIN_TOKEN ?? 'dev-admin-token-change-me');
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  const table = page.getByRole('table', { name: 'Dependencies', exact: true });
  await expect(table.locator('tbody tr').first()).toBeVisible();
  const count = await table.locator('tbody tr').count();
  assert.ok(count >= 3);
  await expect(table.getByText('OK', { exact: true })).toHaveCount(count);
  await expect(page.locator('.console-connection')).toHaveText('Connected');
  await expect(page.getByRole('navigation', { name: 'Breadcrumbs', exact: true })).toHaveText('Infrastructure');
  for (const theme of ['Light theme', 'Dark theme']) {
    await page.getByRole('button', { name: theme, exact: true }).click();
    // Let the shared theme color transition finish before measuring contrast.
    await page.waitForTimeout(350);
    if (process.env.BACKPLANE_INFRA_SCREENSHOTS) await page.screenshot({ path: `${process.env.BACKPLANE_INFRA_SCREENSHOTS}/${theme.startsWith('Light') ? 'light' : 'dark'}.png` });
    const result = await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
    assert.deepEqual(result.violations, [], `${theme}: accessibility`);
  }
  dropUpdates = true;
  await expect(table.getByText('Stale', { exact: true })).toHaveCount(count, { timeout: 22000 });
  await expect(table.getByText('OK', { exact: true })).toHaveCount(0);
  await expect(page.locator('.console-connection')).toHaveText('Connected');
  await expect(page.getByText('Some checks have not updated for more than 15 seconds. Their current health is unknown.', { exact: true })).toBeVisible();
  dropUpdates = false;
  await expect(table.getByText('OK', { exact: true })).toHaveCount(count, { timeout: 15000 });
  // A disconnect invalidates displayed checks immediately; recovery requires
  // a successful read on the new connection, not merely opening another socket.
  disconnected = true;
  clientSocket.close({ code: 1012, reason: 'acceptance disconnect' });
  await expect(table.getByText('Stale', { exact: true })).toHaveCount(count, { timeout: 5000 });
  await expect(page.getByText('Live updates are unavailable. Showing the last received checks until the connection recovers.', { exact: true })).toBeVisible();
  disconnected = false;
  await expect(table.getByText('OK', { exact: true })).toHaveCount(count, { timeout: 30000 });
  await expect(page.locator('.console-connection')).toHaveText('Connected');
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'mobile page must not overflow horizontally');
  const mobile = await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
  assert.deepEqual(mobile.violations, [], 'mobile accessibility');
  if (process.env.BACKPLANE_INFRA_SCREENSHOTS) await page.screenshot({ path: `${process.env.BACKPLANE_INFRA_SCREENSHOTS}/mobile.png` });
  assert.deepEqual(errors, []);
  console.log('Infrastructure acceptance passed: fresh checks, connected-but-stale data, disconnect, recovery, both themes, mobile and accessibility');
} finally { await browser.close(); }
