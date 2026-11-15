import assert from 'node:assert/strict';
import { chromium, expect } from '@playwright/test';

// The Go test owns this isolated server and its in-memory registry/session store.
// Never fall back to a running dev installation: that would test another backend.
const [origin, token] = process.argv.slice(2);
assert.ok(origin && token, 'Usage: console-integration.mjs <test-server-origin> <operator-token>');
const base = new URL('/backplane/', origin).href;
const browser = await chromium.launch();
try {
  const context = await browser.newContext(), page = await context.newPage();
  const errors = [], sockets = [], bundles = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('websocket', (socket) => sockets.push(socket.url()));
  page.on('request', (req) => { if (new URL(req.url()).pathname.includes('/plugins/')) bundles.push(req.url()); });
  await page.goto(new URL('s/hello/settings', base).href);
  const input = page.getByLabel('Operator token', { exact: true });
  await input.fill('invalid-token');
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('This token was not accepted');
  await input.fill(token);
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect(page.getByLabel('Display name')).toBeVisible();
  await expect(page).toHaveURL(new URL('s/hello/settings', base).href);
  const cookie = (await context.cookies()).find((item) => item.name === 'bp_session');
  assert.equal(cookie?.httpOnly, true);
  assert.equal(cookie?.sameSite, 'Strict');
  assert.equal(cookie?.path, '/backplane/');
  assert.ok(bundles.some((url) => url.endsWith('/remoteEntry.js')));
  assert.ok(bundles.every((url) => new URL(url).origin === origin));
  assert.deepEqual(sockets, [new URL('ws', base).href.replace(/^http/, 'ws')]);
  // The Go server enables public ingest keys. The console reports through
  // its authenticated log endpoint, without embedding a key in the bundle.
  const delivered = page.waitForResponse((response) => response.url() === new URL('auth/telemetry/v1/logs', base).href, { timeout: 15_000 });
  await page.evaluate(() => window.dispatchEvent(new ErrorEvent('error', {
    message: 'console-ingest-regression', error: new Error('console-ingest-regression'),
  })));
  assert.equal((await delivered).status(), 200);
  // This service only exists in the Go test's registry, proving the browser
  // consumed the real CatalogService stream rather than the dev installation.
  await page.getByRole('navigation', { name: 'Platform', exact: true }).getByRole('link', { name: 'Services', exact: true }).click();
  await expect(page.getByRole('main').getByRole('link', { name: 'browser-live-data', exact: true })).toBeVisible();
  await page.getByRole('navigation', { name: 'Platform', exact: true }).getByRole('link', { name: 'Infrastructure', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Infrastructure', exact: true })).toBeVisible();
  await expect(page.getByText('PostgreSQL', { exact: true })).toBeVisible();
  await expect(page.getByText('OK', { exact: true })).toBeVisible();
  await expect(page.getByText('Workflows: Disabled', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Light theme', exact: true }).click();
  await expect(page.getByText('OK', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Dark theme', exact: true }).click();
  await page.goto(new URL('services/hello/workflows', base).href);
  await expect(page.getByText('Disabled in this deployment', { exact: true })).toBeVisible();
  await page.goto(new URL('s/hello/settings', base).href);
  await expect(page.getByLabel('Display name')).toBeVisible();
  await expect(input).toHaveCount(0);
  await page.getByRole('button', { name: 'Session', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Log out', exact: true }).click();
  await expect(input).toHaveValue('');
  assert.equal((await context.cookies()).some((item) => item.name === 'bp_session'), false);
  await page.reload();
  await expect(input).toBeVisible();
  assert.deepEqual(errors, []);
  console.log('Go console integration passed: cookie login, catalog stream, bundle proxy, shared socket, deep-link session restore and logout');
} finally { await browser.close(); }
