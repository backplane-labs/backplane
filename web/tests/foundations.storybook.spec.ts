import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';

for (const theme of ['dark', 'light']) {
  test(`${theme}: utilities and subtree recovery`, async ({ page }) => {
    await page.goto(`/iframe.html?id=kit-foundations--utilities-and-recovery&viewMode=story&globals=theme:${theme}`);
    await page.getByRole('button', { name: 'Remove service.name' }).click();
    await expect(page.getByRole('button', { name: 'Remove service.name' })).toHaveCount(0);
    await page.getByRole('combobox', { name: 'Source segment', exact: true }).click();
    await page.getByRole('option', { name: 'Kratos', exact: true }).click();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('combobox', { name: 'Source segment', exact: true })).toContainText('Kratos');
    await page.getByRole('button', { name: 'Fail subtree' }).click();
    await expect(page.getByRole('alert')).toContainText('The module could not render.');
    await page.getByRole('button', { name: 'Recover', exact: true }).click();
    await expect(page.getByText('Recovered content', { exact: true })).toBeVisible();
    expect(
      (await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations,
    ).toEqual([]);
  });
  test(`${theme}: readonly schema values`, async ({ page }) => {
    await page.goto(`/iframe.html?id=kit-schema-forms--read-only&viewMode=story&globals=theme:${theme}`);
    await expect(page.getByLabel('Access token', { exact: true })).toHaveValue('***');
    await expect(page.getByRole('button', { name: 'Save', exact: true })).toHaveCount(0);
    await expect(page.getByLabel('Sequence', { exact: true })).toBeDisabled();
    expect(
      (await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations,
    ).toEqual([]);
  });
}
