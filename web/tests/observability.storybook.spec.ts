import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';

for (const theme of ['dark', 'light'])
  for (const story of ['logs-and-context', 'traces-and-links', 'error-and-causes']) {
    test(`${theme}: observability ${story}`, async ({ page }) => {
      const errors: string[] = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`/iframe.html?id=kit-observability--${story}&viewMode=story&globals=theme:${theme}`);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      if (story === 'logs-and-context') {
        const table = page.getByRole('table', { name: 'Service logs', exact: true });
        expect(await table.locator('tbody tr').count()).toBeLessThan(50);
        await page.getByRole('checkbox', { name: 'Select row log-0', exact: true }).check();
        await page.getByRole('button', { name: 'Reverse logs' }).click();
        await page.getByLabel('Filter Service logs', { exact: true }).fill('Request 0 ');
        await expect(page.getByRole('checkbox', { name: 'Select row log-0', exact: true })).toBeChecked();
        await table.locator('tbody').getByRole('button', { name: 'Details', exact: true }).first().click();
        const dialog = page.getByRole('dialog');
        await expect(dialog.getByRole('cell', { name: '18446744073709551615', exact: true })).toBeVisible();
        await expect(dialog.getByRole('textbox', { name: 'Body', exact: true })).toContainText(
          '<script>unsafe()</script>',
        );
        await dialog.getByRole('button', { name: 'Surrounding logs' }).click();
        await dialog.getByRole('button', { name: 'Open trace' }).click();
        await expect(page.getByLabel('Context log')).toHaveText('log-0');
        await expect(page.getByLabel('Correlation target')).toContainText('0123456789abcdef');
        await page.keyboard.press('Escape');
        await expect(dialog).toHaveCount(0);
      }
      if (story === 'traces-and-links') {
        await expect(page.getByRole('cell', { name: /123456789 UTC/ })).toBeVisible();
        await page.getByRole('button', { name: 'Expand row span-0', exact: true }).click();
        const table = page.getByRole('table', { name: 'Trace spans', exact: true });
        expect(await table.locator('tbody tr').count()).toBeLessThan(50);
        await table.locator('tbody').getByRole('button', { name: 'Details', exact: true }).first().click();
        const dialog = page.getByRole('dialog');
        await expect(dialog.getByText('1790683200123456789', { exact: true })).toBeVisible();
        await dialog.getByText('1790683200123456889 · exception', { exact: true }).click();
        await expect(dialog.getByRole('cell', { name: 'exception.message', exact: true })).toBeVisible();
        await dialog.getByRole('button', { name: 'Open logs' }).click();
        await expect(page.getByLabel('Correlation target')).toContainText('"signal":"logs"');
        await page.keyboard.press('Escape');
        await expect(page.getByText('Some parent relationships are missing, cyclic or duplicated.')).toBeVisible();
      }
      if (story === 'error-and-causes') {
        await expect(page.getByRole('heading', { name: 'DatabaseError' })).toBeVisible();
        await page.getByRole('table', { name: 'DeliveryError', exact: true }).locator('tbody tr').first().focus();
        await page.keyboard.press('Enter');
        await expect(page.getByLabel('Source context')).toContainText('throw new DeliveryError(cause);');
        await expect(page.getByRole('textbox', { name: 'Structured payload', exact: true })).toContainText(
          '18446744073709551615',
        );
      }
      expect(
        (await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze())
          .violations,
      ).toEqual([]);
      expect(errors).toEqual([]);
    });
  }
