import { chooseOption } from './choose-option';
import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
const stories = ['services-and-health', 'configuration', 'audit', 'errors', 'explore', 'operations', 'wiring-yaml', 'wiring-graph', 'wiring-loop', 'wiring-rule', 'workflows', 'schedules', 'events-and-dead-letters', 'automation', 'workflows-workspace', 'system-map-view'];
for (const theme of ['dark', 'light']) for (const story of stories) {
  test(`${theme}: platform ${story}`, async ({ page }) => {
    const errors: string[] = []; page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`/iframe.html?id=kit-platform--${story}&viewMode=story&globals=theme:${theme}`);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    if (story === 'services-and-health') {
      const table = page.getByRole('table', { name: 'Instances', exact: true }); await expect(table.getByRole('cell', { name: 'hello-1', exact: true })).toBeVisible();
      await table.locator('tbody tr').first().focus(); await page.keyboard.press('Enter');
      const drawer = page.getByRole('dialog');
      await expect(drawer.getByText('Waiting for database', { exact: true })).toBeVisible();
      await expect(drawer.getByRole('textbox', { name: 'Effective configuration', exact: true })).toContainText('***');
      await expect(page.getByText('greeter/database').first()).toBeVisible();
      await page.keyboard.press('Escape');
    }
    if (story === 'configuration') {
      const editor = page.getByRole('textbox', { name: 'greeter.suffix', exact: true }); await expect(editor).toContainText('!');
      await editor.fill('"changed"'); await page.getByRole('button', { name: 'Reconnect fixture' }).click(); await expect(editor).toContainText('changed');
      await page.getByRole('checkbox', { name: 'Simulate write failure' }).check(); await page.getByRole('button', { name: 'Save revision', exact: true }).click();
      await expect(page.getByText('The action did not complete successfully. Check its outcome before retrying.')).toBeVisible(); await expect(editor).toContainText('changed');
      await expect(page.getByLabel('Write attempts')).toHaveText('1'); await page.waitForTimeout(350); await expect(page.getByLabel('Write attempts')).toHaveText('1');
      await page.getByRole('checkbox', { name: 'Simulate write failure' }).uncheck(); await page.getByRole('button', { name: 'Validate', exact: true }).click();
      await expect(page.getByText('Validation passed.')).toBeVisible(); await page.getByRole('button', { name: 'Save revision', exact: true }).click(); await expect(page.getByRole('button', { name: 'Save revision', exact: true })).toBeDisabled();
      await expect(page.getByLabel('Write attempts')).toHaveText('2');
    }
    if (story === 'errors') {
      // Groups first: one row per distinct error; a group opens its drawer, an occurrence in it opens its own.
      const table = page.getByRole('table', { name: 'Errors', exact: true });
      await expect(table.locator('tbody tr')).toHaveCount(2);
      await expect(table.getByText('New', { exact: true })).toBeVisible();
      await page.getByRole('button', { name: /^billing/ }).first().click();
      await expect(table.locator('tbody tr')).toHaveCount(1);
      await page.getByRole('button', { name: 'Remove service', exact: true }).click();
      await expect(table.locator('tbody tr')).toHaveCount(2);
      await table.locator('tbody tr').first().focus(); await page.keyboard.press('Enter');
      const group = page.getByRole('dialog');
      await expect(group.getByText('7 stored', { exact: false })).toBeVisible();
      // Triage: resolving takes the group out of the open list; "All states" shows it with its note.
      await group.getByRole('button', { name: 'Resolve', exact: true }).click();
      const confirm = page.getByRole('dialog').last();
      await confirm.getByRole('textbox', { name: 'Note', exact: true }).fill('fixed in 2.0.1');
      await confirm.getByRole('button', { name: 'Resolve', exact: true }).click();
      await expect(group.getByText('Resolved by console:fixture', { exact: false })).toBeVisible();
      await expect(group.getByText('fixed in 2.0.1', { exact: true })).toBeVisible();
      await group.getByRole('table', { name: 'Occurrences of this group', exact: true }).locator('tbody tr').first().click();
      const drawer = page.getByRole('dialog').last();
      await expect(drawer.getByRole('button', { name: 'Copy report', exact: true })).toBeVisible();
      await expect(drawer.getByText('total', { exact: true }).first()).toBeVisible();
      await drawer.getByRole('tab', { name: 'History', exact: true }).click(); await expect(drawer.getByText('navigation /checkout', { exact: true })).toBeVisible();
      await drawer.getByRole('tab', { name: 'State', exact: true }).click(); await expect(drawer.getByText('cart', { exact: true })).toBeVisible();
      await drawer.getByRole('tab', { name: 'Logs', exact: true }).click(); await expect(drawer.getByText('rendering cart').first()).toBeVisible();
      await drawer.getByRole('tab', { name: 'Environment', exact: true }).click(); await expect(drawer.getByText('app-debug.browser@0.1.0', { exact: true })).toBeVisible();
      await page.keyboard.press('Escape'); await page.keyboard.press('Escape');
      await expect(table.locator('tbody tr')).toHaveCount(1);
      await page.getByRole('combobox', { name: 'State', exact: true }).click(); await page.getByRole('option', { name: 'All states', exact: true }).click();
      await expect(table.locator('tbody tr')).toHaveCount(2);
      await expect(table.getByText('Resolved', { exact: true })).toBeVisible();
      // Every occurrence, one by one.
      await page.getByRole('button', { name: 'Occurrences', exact: true }).click();
      await expect(table.locator('tbody tr')).toHaveCount(2);
      await expect(table.getByText('Unhandled', { exact: true })).toBeVisible();
    }
    if (story === 'audit') {
      const table = page.getByRole('table', { name: 'Audit', exact: true });
      await expect(table.locator('tbody tr')).toHaveCount(3);
      await expect(table.getByText('operator · 1b7e2c9a', { exact: true })).toBeVisible();
      await expect(table.getByText('rev 3 · keys greeter.suffix', { exact: true })).toBeVisible();
      // A value in the fields panel keeps it; Alt+click excludes it.
      await page.getByRole('button', { name: /^application/ }).first().click();
      await expect(table.locator('tbody tr')).toHaveCount(2);
      await page.getByRole('button', { name: 'Remove field:source', exact: true }).click();
      await expect(table.locator('tbody tr')).toHaveCount(3);
      // The filter builder: an attribute condition.
      await page.getByRole('button', { name: 'Filter', exact: true }).click();
      await page.getByRole('combobox', { name: 'Field' }).click(); await page.getByRole('option', { name: 'tenant', exact: true }).click();
      await page.getByRole('button', { name: 'acme', exact: true }).click(); await page.getByRole('button', { name: 'Apply', exact: true }).click();
      await expect(table.locator('tbody tr')).toHaveCount(1);
      await table.locator('tbody tr').first().focus(); await page.keyboard.press('Enter');
      const drawer = page.getByRole('dialog'); await expect(drawer.getByRole('button', { name: 'Open trace', exact: true })).toBeVisible();
      await drawer.getByRole('button', { name: 'Exclude attr:tenant', exact: true }).click();
      await expect(table).toHaveCount(0); // tenant is acme and is not acme
    }
    if (story === 'explore') {
      await expect(page.getByRole('button', { name: 'service.name', exact: true })).toBeVisible();
      await expect(page.getByRole('list', { name: 'Logs', exact: true }).getByText('Hello from storage', { exact: true })).toBeVisible();
      await page.getByRole('button', { name: 'Traces', exact: true }).click(); await chooseOption(page, 'Language', 'traceql');
      await page.getByRole('button', { name: 'Run query' }).click(); const traces = page.getByRole('table', { name: 'Traces', exact: true }); await expect(traces.getByText('Greet', { exact: true })).toBeVisible();
      await traces.locator('tbody tr').first().focus(); await page.keyboard.press('Enter');
      await expect(page.getByRole('dialog').getByText('1 span', { exact: true })).toBeVisible();
      await page.keyboard.press('Escape');
    }
    if (story === 'operations') {
      await expect(page.getByLabel('Sequence', { exact: true })).toHaveValue('18446744073709551615');
      await page.getByRole('button', { name: 'Execute', exact: true }).click(); await expect(page.getByLabel('Sent input')).toHaveText('{"name":"World","sequence":18446744073709551615}');
      await expect(page.getByRole('textbox', { name: 'Output', exact: true })).toContainText('hook/hello/Greet/fixture');
    }
    if (story === 'wiring-yaml') {
      const definition = page.getByRole('textbox', { name: 'Definition', exact: true });
      await expect(definition).toContainText('formatter.Format');
      await definition.fill('hook: hello.Greet\nsteps:\n  format:\n    input: {name: req.name}\nresult: {text: format.text}\n');
      await expect(page.getByRole('button', { name: /Every step needs an activity/ })).toBeVisible();
      await expect(page.getByRole('button', { name: /^Save/ })).toBeDisabled();
      await definition.fill('hook: hello.Greet\nsteps:\n  format:\n    activity: formatter.Format\n    input: {name: "req.name + \'!\'"}\nresult: {text: format.text}\n');
      await expect(page.getByText('No problems', { exact: true })).toBeVisible();
      await page.getByRole('textbox', { name: 'Comment', exact: true }).fill('louder');
      await page.getByRole('button', { name: /^Save/ }).click(); await expect(page.getByLabel('Write attempts')).toHaveText('1');
      await page.getByRole('button', { name: 'Versions', exact: true }).click(); await expect(page.getByText('Initial', { exact: true })).toBeVisible();
    }
    if (story === 'wiring-graph') {
      const node = page.locator('.react-flow__node').filter({ hasText: 'formatter.Format' });
      await expect(node).toBeVisible(); await expect(node.getByText('req.name', { exact: true })).toBeVisible();
      await expect(page.locator('.react-flow__edge')).not.toHaveCount(0);
      await node.click(); await expect(page.getByRole('textbox', { name: 'Step name', exact: true })).toHaveValue('format');
      await expect(page.getByText('formatter.Format').first()).toBeVisible();
      await expect(page.getByLabel('Write attempts')).toHaveText('0');
    }
    if (story === 'wiring-loop') {
      const loop = page.locator('.react-flow__node-loop');
      await expect(loop).toBeVisible(); await expect(loop.getByText('req.people', { exact: true })).toBeVisible();
      await expect(page.locator('.react-flow__node').filter({ hasText: 'formatter.Record' })).toBeVisible();
      await loop.getByText('each', { exact: true }).first().click();
      await expect(page.getByRole('textbox', { name: 'Step name', exact: true })).toHaveValue('each');
      await expect(page.getByRole('textbox', { name: 'Item variable', exact: true })).toHaveValue('person');
      await page.locator('.react-flow__node').filter({ hasText: 'formatter.Record' }).click();
      await expect(page.getByRole('textbox', { name: 'Step name', exact: true })).toHaveValue('record');
      await expect(page.getByRole('button', { name: 'in each', exact: true })).toBeVisible();
      await expect(page.getByLabel('Write attempts')).toHaveText('0');
    }
    if (story === 'wiring-rule') {
      await expect(page.getByText('Everything is wired', { exact: true })).toBeVisible();
      await page.getByRole('button', { name: /Greeted rule/ }).click();
      await expect(page.getByRole('textbox', { name: 'Definition', exact: true })).toContainText('formatter.Record');
      await expect(page.getByRole('textbox', { name: 'Rule name', exact: true })).toHaveValue('Greeted rule');
      await page.getByRole('button', { name: 'New binding', exact: true }).click();
      const drawer = page.getByRole('dialog');
      await expect(drawer.getByText('Hooks without a binding', { exact: true })).toBeVisible();
      await drawer.getByRole('button', { name: /hello\.Farewell/ }).click();
      await expect(page.getByRole('textbox', { name: 'Definition', exact: true })).toContainText('hook: hello.Farewell');
    }
    if (story === 'workflows') {
      const table = page.getByRole('table', { name: 'Runs', exact: true }); await expect(table.getByRole('cell', { name: '9007199254740993', exact: true })).toBeVisible();
      await page.getByRole('button', { name: 'Older', exact: true }).click(); await expect(table.getByText('hello/Welcome-2026-09-30T11:00:00Z', { exact: true })).toBeVisible();
      await page.getByRole('button', { name: 'Newer', exact: true }).click(); await expect(table.getByText('welcome-1', { exact: true })).toBeVisible();
      await page.getByRole('button', { name: 'Hook calls', exact: true }).click(); await expect(table.getByText('hook/hello/Greet/k1', { exact: true })).toBeVisible();
      await page.getByRole('button', { name: 'Workflows', exact: true }).click(); await expect(table.getByText('welcome-1', { exact: true })).toBeVisible();
      await table.locator('tbody tr').first().focus(); await page.keyboard.press('Enter');
      const drawer = page.getByRole('dialog'); await expect(drawer.getByText('Activity failed', { exact: true })).toBeVisible();
      await expect(drawer.getByText('console:fixture', { exact: true })).toBeVisible(); await expect(drawer.getByText('hello-1@fixture', { exact: true })).toBeVisible();
      await drawer.getByRole('button', { name: 'Show payload of event 1', exact: true }).click();
      await expect(drawer.getByRole('textbox', { name: 'Payload of event 1', exact: true })).toContainText('18446744073709551615');
      await drawer.getByRole('textbox', { name: 'Signal name', exact: true }).fill('go'); await drawer.getByRole('button', { name: 'Send signal', exact: true }).click();
      await page.getByRole('button', { name: 'Confirm', exact: true }).click(); await expect(page.getByLabel('Sent input')).toHaveText('signal:go:');
      await drawer.getByRole('button', { name: 'Cancel run', exact: true }).click(); await page.getByRole('button', { name: 'Confirm', exact: true }).click();
      await expect(page.getByLabel('Write attempts')).toHaveText('2');
      await drawer.getByRole('button', { name: 'parent-1', exact: true }).click(); await expect(drawer.getByText('Continued as', { exact: true })).toBeVisible();
      await drawer.getByRole('button', { name: 'welcome-1', exact: true }).click(); await expect(drawer.getByText('Activity failed', { exact: true })).toBeVisible();
      await page.keyboard.press('Escape');
    }
    if (story === 'schedules') {
      await expect(page.getByText('Not in Temporal', { exact: true })).toBeVisible(); await expect(page.getByText('Differs from declaration', { exact: true })).toBeVisible();
      const triggers = page.getByRole('button', { name: 'Trigger', exact: true }); await expect(triggers.first()).toBeDisabled();
      await page.getByRole('button', { name: 'Details of Welcome', exact: true }).click(); await page.getByTitle('hello/Welcome-2026-09-30T11:00:00Z', { exact: true }).last().click();
      await expect(page.getByRole('dialog').getByText('Continued as', { exact: true })).toBeVisible(); await page.keyboard.press('Escape');
      await page.getByRole('button', { name: 'Resume', exact: true }).click(); await page.getByRole('textbox', { name: 'Note', exact: true }).fill('maintenance done');
      await page.getByRole('button', { name: 'Confirm', exact: true }).click(); await expect(page.getByLabel('Sent input')).toHaveText('maintenance done');
      await triggers.last().click(); await page.getByRole('button', { name: 'Confirm', exact: true }).click(); await expect(page.getByLabel('Write attempts')).toHaveText('2');
    }
    if (story === 'events-and-dead-letters') {
      await expect(page.getByText('hello.Greeted').first()).toBeVisible();
      await expect(page.getByRole('button', { name: 'Purge selected', exact: true }).first()).toBeDisabled();
      await page.getByRole('checkbox', { name: 'Select #9007199254740993', exact: true }).last().check(); await page.getByRole('button', { name: 'Redrive selected', exact: true }).last().click();
      await page.getByRole('button', { name: 'Confirm', exact: true }).click(); await expect(page.getByLabel('Sent input')).toHaveText('9007199254740993'); await expect(page.getByLabel('Write attempts')).toHaveText('1');
    }
    if (story === 'automation') {
      await expect(page.getByText(/formatter\.Format/).first()).toBeVisible();
      await expect(page.getByText(/formatter\.Record/).first()).toBeVisible();
    }
    if (story === 'workflows-workspace') {
      await expect(page.getByText('Greets a new user')).toBeVisible();
      const table = page.getByRole('table', { name: 'Runs', exact: true }); await expect(table).toBeVisible();
      await expect(page.getByText('No worker is polling task queue hello. Started runs wait until one does.', { exact: true })).toBeVisible();
      await page.getByRole('button', { name: 'Runs of this schedule', exact: true }).last().click();
      await expect(page.getByRole('combobox', { name: 'ID prefix', exact: true })).toContainText('hello/Welcome-');
      await expect(table.locator('tbody tr')).toHaveCount(1); await expect(table.getByText('hello/Welcome-2026-09-30T11:00:00Z', { exact: true })).toBeVisible();
      await page.getByRole('button', { name: 'Clear ID prefix', exact: true }).click(); await expect(table.getByText('welcome-1', { exact: true })).toBeVisible();
      await page.getByRole('button', { name: 'Start Welcome', exact: true }).click();
      const drawer = page.getByRole('dialog'); await drawer.getByRole('textbox', { name: 'Workflow ID (optional)', exact: true }).fill('welcome-1');
      const timeout = drawer.getByRole('textbox', { name: /^Execution timeout/ }); await timeout.fill('5x'); await expect(drawer.getByText('Use a number followed by s, m or h.', { exact: true })).toBeVisible();
      await timeout.fill('5m'); await drawer.getByRole('button', { name: 'Start', exact: true }).click();
      await expect(drawer.getByText('A run with this workflow ID is already running. Open it, or choose another ID.', { exact: true })).toBeVisible();
      await page.keyboard.press('Escape');
    }
    if (story === 'system-map-view') {
      await expect(page.locator('.react-flow__node').filter({ hasText: 'formatter' })).toBeVisible();
      await expect(page.getByRole('heading', { name: /^Connections/ })).toBeVisible();
    }
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(page.getByRole('alertdialog')).toHaveCount(0);
    expect((await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations).toEqual([]);
    expect(errors).toEqual([]);
  });
}
