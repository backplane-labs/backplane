import assert from 'node:assert/strict';
import { chromium, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

const base = `${process.env.BACKPLANE_DEV_URL ?? 'http://127.0.0.1:8081'}/backplane`;
const browser = await chromium.launch();
try {
  const context = await browser.newContext({ viewport: { width: 1440, height: 950 } });
  const page = await context.newPage(), errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.goto(`${base}/`);
  await page.getByLabel('Operator token').fill(process.env.DEV_ADMIN_TOKEN ?? 'dev-admin-token-change-me');
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect(page).toHaveURL(`${base}/services`);
  await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
  const sidebar = page.getByRole('complementary', { name: 'Main navigation' });
  await expect(sidebar.getByRole('link', { name: 'Settings', exact: true })).toBeVisible();
  assert.deepEqual(await sidebar.getByRole('navigation', { name: 'Platform', exact: true }).getByRole('link').allTextContents(), ['Services', 'Wiring', 'Explore', 'Errors', 'Audit', 'Infrastructure']);

  // Services: cards, table and the system map show the same catalog.
  const view = page.getByRole('group', { name: 'View' });
  await view.getByRole('button', { name: 'Cards' }).click();
  await expect(page.getByRole('link', { name: 'hello', exact: true }).first()).toBeVisible();
  await view.getByRole('button', { name: 'Table' }).click();
  await expect(page.getByRole('cell', { name: 'formatter' })).toBeVisible();
  await view.getByRole('button', { name: 'Map' }).click();
  await expect(page.getByRole('heading', { name: /^Connections/ })).toBeVisible();
  await expect(page.locator('.react-flow__node').filter({ hasText: 'formatter' })).toBeVisible();
  await expect(page.locator('.react-flow__edge').first()).toBeAttached();
  await view.getByRole('button', { name: 'Cards' }).click();
  await page.screenshot({ path: '/tmp/backplane-console-dark.png' });
  assert.deepEqual((await new AxeBuilder({ page }).include('.console-shell').withTags(['wcag2a', 'wcag2aa']).analyze()).violations, []);

  // The command palette jumps to any service tab.
  await page.keyboard.press('ControlOrMeta+K');
  await page.getByPlaceholder('Search or jump to…').last().fill('hello Automation');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(`${base}/services/hello/automation`);
  await expect(page.getByText('formatter.Format').first()).toBeVisible();
  // Automation links into Wiring: the binding as a graph and as YAML, checked live.
  await page.getByRole('button', { name: /hello\.Greet/ }).first().click();
  await expect(page).toHaveURL(/\/wiring\?binding=hello\.Greet/);
  await expect(page.locator('.react-flow__node').filter({ hasText: 'formatter.Format' })).toBeVisible();
  await expect(page.locator('.react-flow__edge').first()).toBeAttached();
  await expect(page.getByText('No problems', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'YAML', exact: true }).click();
  await expect(page.getByRole('textbox', { name: 'Definition', exact: true })).toContainText('activity: formatter.Format');
  assert.deepEqual((await new AxeBuilder({ page }).include('.console-shell').withTags(['wcag2a', 'wcag2aa']).analyze()).violations, []);
  await page.goBack();

  // Width can be changed by keyboard or pointer, and survives reload.
  const resize = sidebar.getByRole('separator', { name: 'Navigation width' });
  await resize.focus(); await page.keyboard.press('ArrowRight');
  await expect(resize).toHaveAttribute('aria-valuenow', '280');
  const box = await resize.boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + 80); await page.mouse.down();
  await page.mouse.move(box.x + box.width / 2 + 40, box.y + 80); await page.mouse.up();
  await expect(resize).toHaveAttribute('aria-valuenow', '320');
  await page.reload(); await expect(resize).toHaveAttribute('aria-valuenow', '320');
  await resize.focus(); await page.keyboard.press('Home'); await expect(resize).toHaveAttribute('aria-valuenow', '224');
  await resize.dblclick(); await expect(resize).toHaveAttribute('aria-valuenow', '264');

  await sidebar.getByRole('button', { name: 'Collapse navigation' }).click();
  await page.mouse.move(900, 400);
  await expect(sidebar).toHaveAttribute('data-expanded', 'false');
  assert.equal((await sidebar.boundingBox()).width, 52);
  await page.reload(); await expect(sidebar).toHaveAttribute('data-expanded', 'false');
  await sidebar.hover(); await expect(sidebar).toHaveAttribute('data-expanded', 'true');
  await sidebar.getByRole('button', { name: 'Pin navigation' }).click();
  await page.mouse.move(900, 400); await expect(sidebar).toHaveAttribute('data-expanded', 'true');
  await sidebar.getByRole('button', { name: 'hello', exact: true }).click();
  await expect(sidebar.getByRole('link', { name: 'Settings', exact: true })).toHaveCount(0);
  await sidebar.getByRole('button', { name: 'hello', exact: true }).click();
  await sidebar.getByRole('link', { name: 'Settings', exact: true }).click();
  await expect(page).toHaveURL(`${base}/s/hello/settings`);
  await page.getByLabel('Display name').fill('Navigation check');
  await page.reload(); await expect(page.getByLabel('Display name')).toBeVisible();
  await expect(sidebar.getByRole('link', { name: 'Settings', exact: true })).toHaveAttribute('aria-current', 'page');
  await sidebar.getByRole('button', { name: 'hello', exact: true }).hover();
  await sidebar.getByRole('link', { name: 'Manage hello' }).click();
  await expect(page).toHaveURL(`${base}/services/hello`);
  await expect(page.getByRole('table', { name: 'Instances', exact: true })).toBeVisible();
  await page.getByRole('navigation', { name: 'Manage hello' }).getByRole('link', { name: 'Configuration', exact: true }).click();
  await expect(page.getByRole('switch', { name: 'Override greeter.suffix', exact: true })).toBeVisible();
  const tabs = page.getByRole('navigation', { name: 'Manage hello' });
  for (const tab of ['Automation', 'Operations', 'Events', 'Workflows', 'Telemetry', 'Errors', 'Audit']) {
    await tabs.getByRole('link', { name: tab, exact: true }).click();
    await expect(page).toHaveURL(`${base}/services/hello/${tab.toLowerCase()}`);
  }
  await expect(page.getByRole('table', { name: 'Audit', exact: true })).toBeVisible();
  await sidebar.getByRole('link', { name: 'Explore', exact: true }).click();
  await expect(page.getByRole('textbox', { name: 'Query', exact: true })).toBeVisible();
  await expect(page).toHaveURL(/signal=logs/);
  await sidebar.getByRole('link', { name: 'Audit', exact: true }).click();
  await expect(page.getByRole('table', { name: 'Audit', exact: true })).toBeVisible();
  await sidebar.getByRole('link', { name: 'Services', exact: true }).click();
  await page.getByRole('button', { name: 'Light theme', exact: true }).click();
  await page.reload(); await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await expect(sidebar.getByRole('link', { name: 'Settings', exact: true })).toBeVisible();
  await page.screenshot({ path: '/tmp/backplane-console-light.png' });
  assert.deepEqual((await new AxeBuilder({ page }).include('.console-shell').withTags(['wcag2a', 'wcag2aa']).analyze()).violations, []);
  await page.getByRole('button', { name: 'Dark theme', exact: true }).click();

  // A rejected bundle cannot break platform navigation or another route.
  await page.route('**/plugins/hello/*/plugin.json', (route) => route.fulfill({ json: { name: 'hello', sdk_major: 999, nav: [] } }));
  await page.goto(`${base}/s/hello/settings`);
  await expect(page.getByRole('alert').first()).toContainText('Plugin metadata does not match the catalog');
  await sidebar.getByRole('link', { name: 'Services', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
  await page.unroute('**/plugins/hello/*/plugin.json');
  // Failed logout keeps the shell/session and exposes an explicit retry.
  const logout = async () => { await page.getByRole('button', { name: 'Session', exact: true }).click(); await page.getByRole('menuitem', { name: 'Log out', exact: true }).click(); };
  await page.route('**/auth/logout', (route) => route.fulfill({ status: 503, body: 'Unavailable' }));
  await logout();
  await expect(page.getByRole('alert')).toContainText('Could not log out. Your session is still active.');
  await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
  await page.unroute('**/auth/logout');
  await logout();
  await expect(page.getByLabel('Operator token')).toBeVisible();
  assert.deepEqual(errors, []);
  console.log('Console acceptance passed: live registry, service views and map, palette, wiring graph and YAML, service tabs, module pages, resize, collapse, persistence, themes and accessibility');
} finally { await browser.close(); }
