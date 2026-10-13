import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { request } from 'node:http';
import { chromium, expect as playwrightExpect } from '@playwright/test';

const expect = playwrightExpect.configure({ timeout: 30_000 });
const base = process.env.BACKPLANE_CONSOLE_DEV_URL ?? 'http://127.0.0.1:5173/backplane/';
const browser = await chromium.launch();
const edits = [];
async function edit(relative, transform) {
  const path = new URL(relative, import.meta.url), original = await readFile(path, 'utf8'), modified = transform(original);
  assert.notEqual(modified, original, `HMR test target missing: ${relative}`);
  edits.push({ path, original, modified });
  await writeFile(path, modified);
}
try {
  const context = await browser.newContext(), page = await context.newPage(), errors = [], sockets = [], bundles = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('websocket', (socket) => sockets.push(socket.url()));
  page.on('request', (req) => { if (new URL(req.url()).pathname.includes('/plugins/')) bundles.push(req.url()); });
  await page.goto(new URL('s/hello/settings', base).href);
  await expect(page.getByLabel('Operator token', { exact: true })).toBeVisible();
  await page.getByLabel('Operator token', { exact: true }).fill(process.env.DEV_ADMIN_TOKEN ?? 'dev-admin-token-change-me');
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect(page.getByLabel('Display name')).toBeVisible();
  const cookie = (await context.cookies()).find((cookie) => cookie.name === 'bp_session');
  assert.equal(cookie?.httpOnly, true); assert.equal(cookie?.sameSite, 'Strict');
  assert.equal(cookie?.path, new URL(base).pathname);
  assert.ok(bundles.some((url) => url.endsWith('/remoteEntry.js')));
  assert.ok(bundles.every((url) => new URL(url).origin === new URL(base).origin));
  await page.getByRole('link', { name: 'Overview', exact: true }).click();
  await expect(page.getByTestId('greetings')).toHaveText(/^\d+$/); // Real module RPC through the host socket.
  await page.getByRole('button', { name: 'Refresh statistics' }).click();
  await page.getByRole('link', { name: 'Settings', exact: true }).click();
  await page.reload(); await expect(page.getByLabel('Display name')).toBeVisible();
  await page.getByLabel('Display name').fill('Unsaved HMR draft');
  await page.evaluate(() => { window.__backplaneHmrProbe = 'same-document'; });
  const platformSocketCount = () => sockets.filter((url) => new URL(url).pathname.endsWith('/ws')).length;
  const before = platformSocketCount();
  assert.ok(sockets.some((url) => new URL(url).pathname.endsWith('/__vite_hmr')));
  await edit('../apps/console/src/console/console.css', (css) => `${css}\n.console-brand { --backplane-hmr-check: ready; }\n`);
  await expect.poll(() => page.locator('.console-brand').evaluate((element) => getComputedStyle(element).getPropertyValue('--backplane-hmr-check').trim())).toBe('ready');
  await edit('../apps/console/src/console/Console.tsx', (source) => source.replace('className="console-shell"', 'className="console-shell" data-hmr-check="updated"'));
  await expect(page.locator('.console-shell')).toHaveAttribute('data-hmr-check', 'updated');
  await expect(page.getByLabel('Display name')).toHaveValue('Unsaved HMR draft');
  assert.equal(await page.evaluate(() => window.__backplaneHmrProbe), 'same-document');
  assert.equal(platformSocketCount(), before, 'HMR must not recreate the platform transport');
  const foreign = await context.request.post(new URL('auth/logout', base).href, { headers: { Origin: 'http://foreign.example' } });
  assert.equal(foreign.status(), 404, 'Foreign origins must be rejected by the dev proxy');
  const wsStatus = await new Promise((resolve, reject) => {
    const req = request(new URL('ws', base), { headers: { Origin: 'http://foreign.example', Connection: 'Upgrade', Upgrade: 'websocket', 'Sec-WebSocket-Version': '13', 'Sec-WebSocket-Key': 'dGhlIHNhbXBsZSBub25jZQ==' } });
    req.on('response', (response) => { response.resume(); resolve(response.statusCode); });
    req.on('upgrade', (_, socket) => { socket.destroy(); reject(new Error('Foreign WebSocket origin was accepted')); });
    req.on('error', reject); req.setTimeout(5000, () => req.destroy(new Error('Proxy rejection timed out'))); req.end();
  });
  assert.equal(wsStatus, 404);
  await page.getByRole('button', { name: 'Session', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Log out', exact: true }).click();
  await expect(page.getByLabel('Operator token', { exact: true })).toHaveValue('');
  assert.deepEqual(errors, []);
  console.log('Console dev acceptance passed: cookie login, same-origin bundles, service RPC, deep links, CSS/React HMR without draft/session loss, foreign-origin rejection and logout');
} finally {
  await browser.close();
  for (const { path, original, modified } of edits.reverse()) {
    if (await readFile(path, 'utf8') !== modified) throw new Error(`Concurrent edit detected; refusing to overwrite ${path.pathname}`);
    await writeFile(path, original);
  }
}
