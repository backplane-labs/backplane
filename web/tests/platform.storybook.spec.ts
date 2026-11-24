import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import { chooseOption } from './choose-option';

const stories = ['services-and-health', 'configuration', 'audit', 'errors', 'explore'];
for (const theme of ['dark', 'light'])
  for (const story of stories) {
    test(`${theme}: platform ${story}`, async ({ page }) => {
      const errors: string[] = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`/iframe.html?id=kit-platform--${story}&viewMode=story&globals=theme:${theme}`);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      if (story === 'services-and-health') {
        const table = page.getByRole('table', { name: 'Instances', exact: true });
        await expect(table.getByRole('cell', { name: 'hello-1', exact: true })).toBeVisible();
        await table.locator('tbody tr').first().focus();
        await page.keyboard.press('Enter');
        const drawer = page.getByRole('dialog');
        await expect(drawer.getByText('Waiting for database', { exact: true })).toBeVisible();
        await expect(drawer.getByRole('textbox', { name: 'Effective configuration', exact: true })).toContainText(
          '***',
        );
        await expect(page.getByText('greeter/database').first()).toBeVisible();
        await page.keyboard.press('Escape');
      }
      if (story === 'configuration') {
        const editor = page.getByRole('textbox', { name: 'greeter.suffix', exact: true });
        await expect(editor).toContainText('!');
        await editor.fill('"changed"');
        await page.getByRole('button', { name: 'Reconnect fixture' }).click();
        await expect(editor).toContainText('changed');
        await page.getByRole('checkbox', { name: 'Simulate write failure' }).check();
        await page.getByRole('button', { name: 'Save revision', exact: true }).click();
        await expect(
          page.getByText('The action did not complete successfully. Check its outcome before retrying.'),
        ).toBeVisible();
        await expect(editor).toContainText('changed');
        await expect(page.getByLabel('Write attempts')).toHaveText('1');
        await page.waitForTimeout(350);
        await expect(page.getByLabel('Write attempts')).toHaveText('1');
        await page.getByRole('checkbox', { name: 'Simulate write failure' }).uncheck();
        await page.getByRole('button', { name: 'Validate', exact: true }).click();
        await expect(page.getByText('Validation passed.')).toBeVisible();
        await page.getByRole('button', { name: 'Save revision', exact: true }).click();
        await expect(page.getByRole('button', { name: 'Save revision', exact: true })).toBeDisabled();
        await expect(page.getByLabel('Write attempts')).toHaveText('2');
        // A Live object overrides as a nested form, seeded from the effective value: the nested
        // duration arrives as nanoseconds and becomes a duration.
        await page.getByRole('switch', { name: 'Override greeter.limits', exact: true }).click();
        const limits = page.getByRole('group', { name: 'greeter.limits', exact: true });
        await expect(limits.getByRole('textbox', { name: /Window/ })).toHaveValue('30s');
        // Startup values are read-only, beside their declaration.
        const startup = page.getByRole('region', { name: 'Startup settings', exact: true });
        await expect(startup.getByText('greeter.port', { exact: true })).toBeVisible();
        await expect(startup.getByText('8080', { exact: true })).toBeVisible();
        await expect(startup.getByText('default 8080', { exact: true })).toBeVisible();
        await expect(startup.getByText('greeter.timeout', { exact: true })).toBeVisible();
        await expect(startup.getByText('5s', { exact: true })).toBeVisible();
        await expect(startup.getByText('"***"', { exact: true })).toBeVisible();
        await expect(startup.getByRole('textbox')).toHaveCount(0);
      }
      if (story === 'errors') {
        // Groups first: one row per distinct error; a group opens its drawer, an occurrence in it opens its own.
        const table = page.getByRole('table', { name: 'Errors', exact: true });
        await expect(table.locator('tbody tr')).toHaveCount(2);
        await expect(table.getByText('New', { exact: true })).toBeVisible();
        await page
          .getByRole('button', { name: /^billing/ })
          .first()
          .click();
        await expect(table.locator('tbody tr')).toHaveCount(1);
        await page.getByRole('button', { name: 'Remove service', exact: true }).click();
        await expect(table.locator('tbody tr')).toHaveCount(2);
        await table.locator('tbody tr').first().focus();
        await page.keyboard.press('Enter');
        const group = page.getByRole('dialog');
        await expect(group.getByText('7 stored', { exact: false })).toBeVisible();
        // Triage: resolving takes the group out of the open list; "All states" shows it with its note.
        await group.getByRole('button', { name: 'Resolve', exact: true }).click();
        const confirm = page.getByRole('dialog').last();
        await confirm.getByRole('textbox', { name: 'Note', exact: true }).fill('fixed in 2.0.1');
        await confirm.getByRole('button', { name: 'Resolve', exact: true }).click();
        await expect(group.getByText('Resolved by console:fixture', { exact: false })).toBeVisible();
        await expect(group.getByText('fixed in 2.0.1', { exact: true })).toBeVisible();
        await group
          .getByRole('table', { name: 'Occurrences of this group', exact: true })
          .locator('tbody tr')
          .first()
          .click();
        const drawer = page.getByRole('dialog').last();
        await expect(drawer.getByRole('button', { name: 'Copy report', exact: true })).toBeVisible();
        await expect(drawer.getByText('total', { exact: true }).first()).toBeVisible();
        await drawer.getByRole('tab', { name: 'History', exact: true }).click();
        await expect(drawer.getByText('navigation /checkout', { exact: true })).toBeVisible();
        await drawer.getByRole('tab', { name: 'State', exact: true }).click();
        await expect(drawer.getByText('cart', { exact: true })).toBeVisible();
        await drawer.getByRole('tab', { name: 'Logs', exact: true }).click();
        await expect(drawer.getByText('rendering cart').first()).toBeVisible();
        await drawer.getByRole('tab', { name: 'Environment', exact: true }).click();
        await expect(drawer.getByText('app-debug.browser@0.1.0', { exact: true })).toBeVisible();
        // An upstream installation's tenant: a fact of the occurrence and a facet of the list.
        await expect(drawer.getByText('inst-7', { exact: true })).toBeVisible();
        await page.keyboard.press('Escape');
        await page.keyboard.press('Escape');
        await expect(page.getByRole('button', { name: /^inst-7/ }).first()).toBeVisible();
        await expect(table.locator('tbody tr')).toHaveCount(1);
        await page.getByRole('combobox', { name: 'State', exact: true }).click();
        await page.getByRole('option', { name: 'All states', exact: true }).click();
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
        await page
          .getByRole('button', { name: /^application/ })
          .first()
          .click();
        await expect(table.locator('tbody tr')).toHaveCount(2);
        await page.getByRole('button', { name: 'Remove field:source', exact: true }).click();
        await expect(table.locator('tbody tr')).toHaveCount(3);
        // The filter builder: an attribute condition.
        await page.getByRole('button', { name: 'Filter', exact: true }).click();
        await page.getByRole('combobox', { name: 'Field' }).click();
        await page.getByRole('option', { name: 'tenant', exact: true }).click();
        await page.getByRole('button', { name: 'acme', exact: true }).click();
        await page.getByRole('button', { name: 'Apply', exact: true }).click();
        await expect(table.locator('tbody tr')).toHaveCount(1);
        await table.locator('tbody tr').first().focus();
        await page.keyboard.press('Enter');
        const drawer = page.getByRole('dialog');
        await expect(drawer.getByRole('button', { name: 'Open trace', exact: true })).toBeVisible();
        await drawer.getByRole('button', { name: 'Exclude attr:tenant', exact: true }).click();
        await expect(table).toHaveCount(0); // tenant is acme and is not acme
      }
      if (story === 'explore') {
        await expect(page.getByRole('button', { name: 'service.name', exact: true })).toBeVisible();
        await expect(
          page.getByRole('list', { name: 'Logs', exact: true }).getByText('Hello from storage', { exact: true }),
        ).toBeVisible();
        await page.getByRole('button', { name: 'Traces', exact: true }).click();
        await chooseOption(page, 'Language', 'traceql');
        await page.getByRole('button', { name: 'Run query' }).click();
        const traces = page.getByRole('table', { name: 'Traces', exact: true });
        await expect(traces.getByText('Greet', { exact: true })).toBeVisible();
        await traces.locator('tbody tr').first().focus();
        await page.keyboard.press('Enter');
        await expect(page.getByRole('dialog').getByText('1 span', { exact: true })).toBeVisible();
        await page.keyboard.press('Escape');
      }
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await expect(page.getByRole('alertdialog')).toHaveCount(0);
      expect(
        (await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze())
          .violations,
      ).toEqual([]);
      expect(errors).toEqual([]);
    });
  }
