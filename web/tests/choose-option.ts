import type { Page } from '@playwright/test';

export async function chooseOption(page: Page, name: string, value: string | { label: string }) {
  await page.getByRole('combobox', { name, exact: true }).click();
  const option =
    typeof value === 'string'
      ? page.getByRole('option').and(page.locator(`[data-value=${JSON.stringify(value)}]`))
      : page.getByRole('option', { name: value.label, exact: true });
  await option.click();
}
