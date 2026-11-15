import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';

for (const theme of ['dark', 'light'])
  test(`${theme}: search, stack, log and trace investigation`, async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(
      `/iframe.html?id=workflows-error-investigation--search-to-trace&viewMode=story&globals=theme:${theme}`,
    );
    await page.getByRole('searchbox', { name: 'Search errors' }).fill('request 42');
    await expect(page.getByRole('cell', { name: '18446744073709551615', exact: true })).toBeVisible();
    await page.getByRole('cell', { name: 'Delivery failed for request 42' }).click();
    await expect(page.getByText('Connection refused', { exact: true })).toBeVisible();
    await page.getByRole('cell', { name: 'deliver', exact: true }).click();
    await expect(page.getByText('12: throw new DeliveryError(result.cause);', { exact: false })).toBeVisible();
    await page.getByRole('tab', { name: 'Related logs' }).click();
    await page.getByRole('cell', { name: 'Delivery failed for request 42' }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Open trace' }).click();
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(page.getByRole('cell', { name: '99 ns', exact: true })).toHaveCount(2);
    await page
      .getByRole('region', { name: 'Related spans', exact: true })
      .locator('tbody')
      .getByRole('button', { name: 'Details', exact: true })
      .click();
    await expect(page.getByRole('dialog')).toContainText('1790683200123456789');
    await page.getByRole('dialog').getByRole('button', { name: 'Open logs' }).click();
    await expect(page.getByRole('tab', { name: 'Related logs' })).toHaveAttribute('aria-selected', 'true');
    await page.getByRole('button', { name: 'Back to errors' }).click();
    await expect(page.getByRole('searchbox', { name: 'Search errors' })).toHaveValue('request 42');
    expect(
      (await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations,
    ).toEqual([]);
    expect(errors).toEqual([]);
  });
