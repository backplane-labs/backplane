import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';

for (const mode of ['dark', 'light'] as const) {
  test(`${mode}: common tokens, form overlays, table and editor`, async ({ page }) => {
    const failures: string[] = [];
    page.on('pageerror', (error) => failures.push(error.message));
    page.on('response', (response) => {
      if (response.status() >= 400) failures.push(`${response.status()} ${response.url()}`);
      if (response.url().endsWith('.svg') && !response.headers()['content-type']?.includes('image/svg+xml'))
        failures.push(`Invalid SVG response: ${response.url()}`);
    });
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Component compatibility' })).toBeVisible();
    if (mode === 'light') await page.getByRole('button', { name: 'Light theme' }).click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', mode);
    await expect(
      page.locator('[data-testid="icon-clock-nine"] path, [data-testid="icon-clock-nine"] circle').first(),
    ).toBeVisible();
    await page.getByText('Last 15 minutes', { exact: true }).click();
    await page.getByText('Last 5 minutes', { exact: true }).click();
    await expect(page.getByText('Last 5 minutes', { exact: true })).toBeVisible();
    await expect(page.getByRole('region', { name: 'Requests', exact: true })).toContainText('1,284');
    await page.getByLabel('Filter sources').fill('hello');
    await expect(page.getByRole('row')).toHaveCount(2);
    await page.getByLabel('Filter sources').clear();
    await page.getByRole('button', { name: 'Inspect source' }).click();
    await page.getByLabel('Display name').fill('identity');
    await page.getByRole('combobox', { name: 'Signal' }).click();
    await page.getByRole('option', { name: 'Traces', exact: true }).click();
    await page.getByRole('button', { name: 'Apply', exact: true }).click();
    await expect(page.getByRole('status')).toHaveText('Applied: identity');
    await expect(page.getByRole('button', { name: 'Inspect source' })).toBeFocused();
    await page.getByRole('button', { name: 'Inspect source' }).click();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await page.getByLabel('LogsQL query').fill('_time:1h');
    await page.getByRole('button', { name: mode === 'dark' ? 'Light theme' : 'Dark theme' }).click();
    await expect(page.getByLabel('LogsQL query')).toContainText('_time:1h');
    await page.getByRole('button', { name: mode === 'dark' ? 'Dark theme' : 'Light theme' }).click();
    const accessibility = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
    expect(accessibility.violations).toEqual([]);
    expect(failures).toEqual([]);
    await page.screenshot({ path: `test-results/compatibility-${mode}.png`, fullPage: true });
  });
}
