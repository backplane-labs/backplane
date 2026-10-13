import assert from 'node:assert/strict';
import { chromium, expect } from '@playwright/test';
const origin = process.env.BACKPLANE_DEV_URL ?? 'http://127.0.0.1:8081', base = `${origin}/backplane`;
// Service requests go directly to hello, independently of the console proxy.
const serviceOrigin = process.env.BACKPLANE_HELLO_URL ?? 'http://127.0.0.1:8080';
const browser = await chromium.launch();
try {
  const context = await browser.newContext(), page = await context.newPage(), failures = [], sockets = [];
  page.on('pageerror', (error) => failures.push(error.message));
  page.on('websocket', (socket) => { if (new URL(socket.url()).pathname.endsWith('/ws')) sockets.push(socket.url()); });
  await page.goto(`${base}/s/hello/`);
  await page.getByLabel('Operator token').fill(process.env.DEV_ADMIN_TOKEN ?? 'dev-admin-token-change-me');
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Hello module' })).toBeVisible();
  await expect(page.getByTestId('greetings')).toHaveText(/^\d+$/);
  const before = BigInt(await page.getByTestId('greetings').innerText());
  const greeting = await fetch(`${serviceOrigin}/hello/?name=BrowserAcceptance`);
  assert.equal(greeting.status, 200); assert.match(await greeting.text(), /Welcome, BrowserAcceptance!/);
  await expect(async () => {
    await page.getByRole('button', { name: 'Refresh statistics' }).click();
    assert.ok(BigInt(await page.getByTestId('greetings').innerText()) > before);
    assert.ok(BigInt(await page.getByTestId('audited').innerText()) > 0n);
  }).toPass({ timeout: 15000 });
  assert.equal(sockets.length, 1, 'platform and module clients must use one host socket');
  await page.goto(`${base}/dev`);
  await page.evaluate(() => { window.__backplaneWorkflowDocument = 'same-document'; });
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
  const revisionItem = page.getByRole('button').filter({ hasText: `acceptance ${suffix}` }).first();
  await expect(revisionItem).toBeVisible();
  const revision = /#(\d+)/.exec(await revisionItem.innerText())[1];
  await expect(page.getByText(`rev ${revision} applied`).first()).toBeVisible({ timeout: 20000 });
  // Leave the development installation's prior override intact.
  if (hadOverride) await field.fill(previousSuffix);
  else await override.click();
  await page.getByRole('button', { name: 'Save revision', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Save revision', exact: true })).toBeDisabled();
  await page.getByRole('button', { name: 'Wiring', exact: true }).click();
  await expect(page.getByText('formatter.Format').first()).toBeVisible();
  await page.getByRole('button', { name: 'Operations', exact: true }).click();
  await page.getByRole('button', { name: /^Greet/ }).click();
  await page.getByRole('button', { name: 'Set value', exact: true }).click();
  await page.getByRole('textbox', { name: 'name *', exact: true }).fill('BrowserOperation');
  await page.getByRole('button', { name: 'Execute', exact: true }).click();
  await expect(page.getByRole('textbox', { name: 'Output', exact: true })).toContainText('Welcome, BrowserOperation!', { timeout: 45000 });
  await page.getByRole('button', { name: 'Audit', exact: true }).click();
  await expect(page.getByRole('table', { name: 'Audit', exact: true }).locator('tbody tr').first()).toBeVisible();
  // Application audit: hello marks each greeting backplane.audit; the Collector forwards it and backplane keeps it.
  await page.getByRole('textbox', { name: 'Search text', exact: true }).fill('BrowserAcceptance');
  await expect(async () => {
    await page.getByRole('button', { name: 'Refresh', exact: true }).first().click();
    await expect(page.getByRole('table', { name: 'Audit', exact: true }).getByText('name/BrowserAcceptance', { exact: true }).first()).toBeVisible({ timeout: 1000 });
  }).toPass({ timeout: 30000 });
  await page.getByRole('button', { name: 'Explore', exact: true }).click();
  await page.getByRole('button', { name: 'Run query', exact: true }).click();
  await expect(page.getByRole('list', { name: 'Logs', exact: true }).getByRole('listitem').first()).toBeVisible({ timeout: 20000 });
  await page.getByRole('button', { name: 'Metrics', exact: true }).click();
  await page.getByRole('textbox', { name: 'Query', exact: true }).fill('{__name__!=""}');
  await page.getByRole('button', { name: 'Run query', exact: true }).click();
  await expect(page.locator('canvas').first()).toBeVisible({ timeout: 20000 });
  assert.equal(await page.evaluate(() => window.__backplaneWorkflowDocument), 'same-document', 'Lazy editors/charts must not reload the page');
  // Workflows: start GreetMany, then create and trigger an administrative schedule.
  await page.goto(`${base}/services/hello/workflows`);
  const runRows = page.getByRole('table', { name: 'Runs', exact: true }).locator('tbody tr');
  const runsRefresh = page.locator('section').filter({ has: page.getByRole('group', { name: 'Run kind', exact: true }) }).getByRole('button', { name: 'Refresh', exact: true });
  await page.getByRole('button', { name: 'Start GreetMany', exact: true }).click();
  const start = page.getByRole('dialog');
  await start.getByRole('button', { name: 'Set value', exact: true }).click();
  await start.getByRole('button', { name: 'Add item', exact: true }).click();
  await start.getByRole('textbox').last().fill('BrowserWorkflow');
  await start.getByRole('button', { name: 'Start', exact: true }).click();
  const started = start.getByRole('status').filter({ hasText: 'console/hello/GreetMany/' });
  await expect(started).toBeVisible({ timeout: 20000 });
  const workflowId = (await started.locator('.font-mono').innerText()).trim();
  await started.getByRole('button', { name: 'Open run', exact: true }).click();
  await expect(page.getByRole('dialog').last().getByRole('textbox', { name: 'Output', exact: true })).toContainText('BrowserWorkflow', { timeout: 30000 });
  await page.keyboard.press('Escape'); await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(async () => {
    await runsRefresh.click();
    await expect(runRows.filter({ hasText: workflowId })).toHaveCount(1, { timeout: 2000 });
  }).toPass({ timeout: 30000 });
  // Schedules belong to the administrator; the SDK does not create HourlyReport.
  const scheduleName = `BrowserReport-${Date.now()}`;
  await page.getByRole('button', { name: 'Create schedule', exact: true }).click();
  const editor = page.getByRole('dialog');
  await editor.getByRole('textbox', { name: 'Schedule name', exact: true }).fill(scheduleName);
  await editor.getByRole('combobox', { name: 'Workflow', exact: true }).click();
  await page.getByRole('option', { name: 'Report', exact: true }).click();
  await editor.getByRole('checkbox', { name: 'Create paused', exact: true }).check();
  await editor.getByRole('button', { name: 'Save schedule', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  const report = page.getByRole('group', { name: scheduleName, exact: true });
  try {
    await expect(report.getByText('Paused', { exact: true })).toBeVisible();
    await report.getByRole('button', { name: 'Runs of this schedule', exact: true }).click();
    await expect(page.getByRole('combobox', { name: 'ID prefix', exact: true })).toContainText(`hello/${scheduleName}-`);
    await expect(page.getByRole('table', { name: 'Runs', exact: true }).or(page.getByText('No runs match.', { exact: true }))).toBeVisible();
    const previousRuns = new Set(await runRows.locator('td:nth-child(2) > div:first-child').allInnerTexts());
    await report.getByRole('button', { name: 'Trigger', exact: true }).click();
    await page.getByRole('button', { name: 'Confirm', exact: true }).click();
    await expect(page.getByRole('dialog')).toHaveCount(0);
    let triggered;
    await expect(async () => {
      await runsRefresh.click();
      await expect(page.getByRole('table', { name: 'Runs', exact: true })).toBeVisible({ timeout: 2000 });
      triggered = (await runRows.locator('td:nth-child(2) > div:first-child').allInnerTexts()).find((id) => !previousRuns.has(id));
      assert.ok(triggered?.startsWith(`hello/${scheduleName}-`), 'triggered run listed');
    }).toPass({ timeout: 30000 });
    await runRows.filter({ hasText: triggered }).click();
    await expect(page.getByRole('dialog').getByText('Report', { exact: true })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(report.getByText('Paused', { exact: true })).toBeVisible();
  } finally {
    await page.keyboard.press('Escape');
    await report.getByRole('button', { name: 'Delete schedule', exact: true }).click();
    await page.getByRole('button', { name: 'Confirm', exact: true }).click();
    await expect(report).toHaveCount(0);
  }
  await page.getByRole('button', { name: 'Light theme', exact: true }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.getByRole('button', { name: 'Session', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Log out', exact: true }).click();
  await expect(page.getByLabel('Operator token')).toBeVisible();
  assert.deepEqual(failures, []);
  console.log('Live dev acceptance passed: service relay, config, binding, hook, audit (platform and application), stored logs/metrics, workflow start and schedule trigger, both themes and logout');
} catch (error) { console.error(error); process.exitCode = 1; }
finally { await browser.close(); }
