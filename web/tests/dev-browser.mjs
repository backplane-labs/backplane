import assert from 'node:assert/strict';
import { chromium, expect } from '@playwright/test';

const origin = process.env.BACKPLANE_DEV_URL ?? 'http://127.0.0.1:8081',
  base = `${origin}/backplane`;
// Service requests go directly to hello, independently of the console proxy.
const serviceOrigin = process.env.BACKPLANE_HELLO_URL ?? 'http://127.0.0.1:8080';
const browser = await chromium.launch();
try {
  const context = await browser.newContext(),
    page = await context.newPage(),
    failures = [],
    sockets = [];
  page.on('pageerror', (error) => failures.push(error.message));
  page.on('websocket', (socket) => {
    if (new URL(socket.url()).pathname.endsWith('/ws')) sockets.push(socket.url());
  });
  await page.goto(`${base}/s/hello/`);
  await page.getByLabel('Operator token').fill(process.env.DEV_ADMIN_TOKEN ?? 'dev-admin-token-change-me');
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Hello module' })).toBeVisible();
  await expect(page.getByTestId('greetings')).toHaveText(/^\d+$/);
  const before = BigInt(await page.getByTestId('greetings').textContent());
  const greeting = await fetch(`${serviceOrigin}/hello/?name=BrowserAcceptance`);
  assert.equal(greeting.status, 200);
  assert.match(await greeting.text(), /Hello, BrowserAcceptance!/);
  await expect(async () => {
    await page.getByRole('button', { name: 'Refresh statistics' }).click();
    assert.ok(BigInt(await page.getByTestId('greetings').textContent()) > before);
  }).toPass({ timeout: 15000 });
  assert.equal(sockets.length, 1, 'platform and module clients must use one host socket');
  await page.goto(`${base}/dev`);
  await page.evaluate(() => {
    window.__backplaneDevDocument = 'same-document';
  });
  await page.getByRole('button', { name: 'Configuration', exact: true }).click();
  const override = page.getByRole('switch', { name: 'Override greeter.suffix', exact: true });
  await expect(override).toBeVisible();
  const hadOverride = (await override.getAttribute('aria-checked')) === 'true';
  const field = page.getByRole('textbox', { name: 'greeter.suffix', exact: true });
  const previousSuffix = hadOverride ? await field.inputValue() : undefined;
  if (!hadOverride) await override.click();
  const suffix = `b${Date.now().toString(36).slice(-6)}`;
  await field.fill(suffix);
  await page.getByLabel('Comment', { exact: true }).fill(`acceptance ${suffix}`);
  await page.getByRole('button', { name: 'Validate', exact: true }).click();
  await expect(page.getByText('Validation passed.', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Save revision', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Save revision', exact: true })).toBeDisabled();
  const revisionItem = page
    .getByRole('button')
    .filter({ hasText: `acceptance ${suffix}` })
    .first();
  await expect(revisionItem).toBeVisible();
  const revision = /#(\d+)/.exec(await revisionItem.textContent())[1];
  await expect(page.getByText(`rev ${revision} applied`).first()).toBeVisible({ timeout: 20000 });
  // Leave the development installation's prior override intact.
  if (hadOverride) await field.fill(previousSuffix);
  else await override.click();
  await page.getByRole('button', { name: 'Save revision', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Save revision', exact: true })).toBeDisabled();
  await page.getByRole('button', { name: 'Audit', exact: true }).click();
  await expect(page.getByRole('table', { name: 'Audit', exact: true }).locator('tbody tr').first()).toBeVisible();
  // Application audit: hello marks each greeting backplane.audit; the Collector forwards it and backplane keeps it.
  await page.getByRole('textbox', { name: 'Search text', exact: true }).fill('BrowserAcceptance');
  await expect(async () => {
    await page.getByRole('button', { name: 'Refresh', exact: true }).first().click();
    await expect(
      page
        .getByRole('table', { name: 'Audit', exact: true })
        .getByText('name/BrowserAcceptance', { exact: true })
        .first(),
    ).toBeVisible({ timeout: 1000 });
  }).toPass({ timeout: 30000 });
  await page.getByRole('button', { name: 'Explore', exact: true }).click();
  await page.getByRole('button', { name: 'Run query', exact: true }).click();
  await expect(page.getByRole('list', { name: 'Logs', exact: true }).getByRole('listitem').first()).toBeVisible({
    timeout: 20000,
  });
  await page.getByRole('button', { name: 'Metrics', exact: true }).click();
  await page.getByRole('textbox', { name: 'Query', exact: true }).fill('sum({__name__="hello.greetings"})');
  await page.getByRole('button', { name: 'Run query', exact: true }).click();
  await expect(page.locator('canvas').first()).toBeVisible({ timeout: 20000 });
  assert.equal(
    await page.evaluate(() => window.__backplaneDevDocument),
    'same-document',
    'Lazy editors/charts must not reload the page',
  );
  await page.getByRole('button', { name: 'Light theme', exact: true }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.getByRole('button', { name: 'Session', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Log out', exact: true }).click();
  await expect(page.getByLabel('Operator token')).toBeVisible();
  assert.deepEqual(failures, []);
  process.stdout.write(
    'Live dev acceptance passed: service relay, config, audit (platform and application), stored logs/metrics, both themes and logout\n',
  );
} catch (error) {
  process.stderr.write(`${error}\n`);
  process.exitCode = 1;
} finally {
  await browser.close();
}
