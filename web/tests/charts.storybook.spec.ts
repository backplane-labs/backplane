import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';

for (const theme of ['dark', 'light'])
  for (const story of ['plots-and-data', 'values-and-controls', 'heatmap-data']) {
    test(`${theme}: charts ${story}`, async ({ page }) => {
      const errors: string[] = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto(`/iframe.html?id=kit-charts--${story}&viewMode=story&globals=theme:${theme}`);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      if (story === 'plots-and-data') {
        await expect(page.getByText('Showing a bounded sample of the data.')).toBeVisible();
        const chart = page.getByRole('region', { name: 'Requests over time', exact: true }).nth(1);
        await chart.getByRole('button', { name: 'Sequence', exact: true }).click();
        await expect(chart.getByRole('button', { name: 'Sequence', exact: true })).toHaveAttribute(
          'aria-pressed',
          'false',
        );
        await chart.getByRole('button', { name: 'Show data' }).click();
        await chart.getByRole('button', { name: 'Filters', exact: true }).click();
        await chart.getByLabel('Filter column Value', { exact: true }).fill('18446744073709551615');
        await expect(chart.getByRole('cell', { name: '18446744073709551615', exact: true }).first()).toBeVisible();
        await chart.getByLabel('Range start', { exact: true }).fill('10');
        await chart.getByLabel('Range end', { exact: true }).fill('20');
        await chart.getByRole('button', { name: 'Apply range' }).click();
        await expect(page.getByLabel('Selected range')).toHaveText('{"from":10,"to":20}');
      }
      if (story === 'values-and-controls') {
        await expect(page.getByRole('status', { name: 'Sequence number', exact: true })).toContainText(
          '18446744073709551615',
        );
        await page.getByRole('button', { name: 'Add threshold' }).click();
        await expect(page.getByLabel('Threshold value 2', { exact: true })).toHaveValue('80');
        await page.getByLabel('Threshold value 2', { exact: true }).fill('90');
        await expect(page.getByLabel('Visualization settings')).toContainText('90');
        await page.getByRole('button', { name: 'Remove threshold' }).last().click();
        await expect(page.getByLabel('Threshold value 2', { exact: true })).toHaveCount(0);
      }
      if (story === 'heatmap-data') {
        await expect(page.getByText('Partial results', { exact: true })).toBeVisible();
        expect(await page.locator('svg[aria-label="Latency buckets"] rect').count()).toBe(500);
        await page.getByRole('table', { name: 'Latency buckets', exact: true }).locator('tbody tr').first().focus();
        await page.keyboard.press('Enter');
        await expect(page.getByLabel('Selected cell')).toHaveText('0');
      }
      expect(
        (await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze())
          .violations,
      ).toEqual([]);
      expect(errors).toEqual([]);
    });
  }
