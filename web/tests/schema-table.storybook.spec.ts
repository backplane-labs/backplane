import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

for (const theme of ['dark', 'light']) {
  test(`${theme}: schema table columns, cells, cursor pages and row actions`, async ({ page }) => {
    await page.goto(`/iframe.html?id=kit-schema-table--paginated&viewMode=story&globals=theme:${theme}`);
    const table = page.getByRole('table', { name: 'Devices' });
    await expect(table.getByText('Device 4', { exact: true })).toBeVisible();
    await expect(table.getByRole('columnheader')).toHaveText(['Name', 'Status', 'OS', 'Sessions', 'Trusted', 'Last seen', 'ID', 'Push token', 'Actions']);
    await expect(table.getByText('Revoked', { exact: true })).toBeVisible();
    await expect(table.getByText('18446744073709551614', { exact: true })).toBeVisible();
    await expect(table.getByText('de000000-0000-0000-0000-000000000001', { exact: true })).toBeAttached();
    await expect(table.getByText('token-1')).toHaveCount(0);
    await page.getByRole('button', { name: 'Load more' }).click();
    await expect(page.getByText('8 rows')).toBeVisible();
    await page.getByRole('button', { name: 'Load more' }).click();
    await expect(page.getByText('12 rows')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Load more' })).toHaveCount(0);
    await table.getByRole('button', { name: 'Open' }).first().click();
    await expect(page.getByText('Opened Device 1')).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect((await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations).toEqual([]);
  });
}
