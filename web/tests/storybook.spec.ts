import { chooseOption } from './choose-option';
import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

for (const theme of ['dark', 'light']) {
  for (const story of ['controls', 'large-table', 'paginated-table', 'tree', 'upload', 'time', 'forms', 'states']) {
    test(`${theme}: compositions ${story}`, async ({ page }) => {
      const failures: string[] = [];
      page.on('pageerror', (error) => failures.push(error.message));
      await page.goto(`/iframe.html?id=kit-compositions--${story}&viewMode=story&globals=theme:${theme}`);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      if (story === 'controls') {
        await page.getByRole('button', { name: 'Increase Exact amount' }).click();
        await expect(page.getByLabel('Exact amount', { exact: true })).toHaveValue('9007199254740993.126');
        await page.getByRole('combobox', { name: 'Remote service' }).click();
        await page.getByRole('combobox', { name: 'Search Remote service' }).fill('slow');
        await page.getByRole('combobox', { name: 'Search Remote service' }).fill('fast');
        await page.getByRole('option', { name: 'fast first' }).click();
        await page.keyboard.press('Escape');
        await expect(page.getByRole('combobox', { name: 'Remote service', exact: true })).toContainText('fast first');
        await page.getByRole('button', { name: 'Remove source', exact: true }).click();
        await page.getByRole('button', { name: 'Confirm', exact: true }).click();
        await expect(page.getByText('Source removed', { exact: true })).toBeVisible();
      }
      if (story === 'large-table') {
        await expect(page.getByRole('cell', { name: '9007199254740993', exact: true })).toBeVisible();
        expect(await page.getByRole('table', { name: 'Services', exact: true }).locator('tbody tr').count()).toBeLessThan(50);
        await page.getByRole('checkbox', { name: 'Select row row-0', exact: true }).check();
        await page.getByRole('button', { name: 'Reverse data' }).click();
        await expect(page.getByLabel('Selected IDs')).toHaveText('row-0');
        await page.getByLabel('Filter Services', { exact: true }).fill('Service 0');
        await expect(page.getByRole('checkbox', { name: 'Select row row-0', exact: true })).toBeChecked();
        await page.getByLabel('Filter Services', { exact: true }).fill('');
        await expect(page.getByRole('cell', { name: 'Service 9999', exact: true })).toBeVisible();
        await page.getByRole('table', { name: 'Services', exact: true }).locator('tbody tr').first().focus();
        await page.keyboard.press('End');
        await expect(page.locator('[data-row-id="row-0"]')).toBeFocused();
        await page.getByRole('button', { name: 'Columns', exact: true }).click();
        await page.getByLabel('Show State').uncheck();
        await chooseOption(page, 'Pin Service', 'left');
        await page.keyboard.press('Escape');
        await expect(page.getByRole('columnheader', { name: /^State/ })).toHaveCount(0);
      }
      if (story === 'paginated-table') {
        await page.getByRole('button', { name: 'Next', exact: true }).click();
        await expect(page.getByRole('cell', { name: 'Service 25', exact: true })).toBeVisible();
        await page.getByRole('button', { name: 'Previous', exact: true }).click();
        await expect(page.getByRole('cell', { name: 'Service 0', exact: true })).toBeVisible();
      }
      if (story === 'tree') {
        expect(await page.getByRole('treeitem').count()).toBeLessThan(50);
        await page.getByRole('treeitem', { name: 'Services', exact: true }).focus();
        await page.keyboard.press('ArrowRight');
        await expect(page.getByRole('treeitem', { name: 'Service 0', exact: true })).toBeFocused();
        await page.keyboard.press('End');
        await expect(page.getByRole('treeitem', { name: 'Service 9999', exact: true })).toBeFocused();
        await page.keyboard.press('Space');
        await expect(page.getByLabel('Selected resource')).toHaveText('row-9999');
      }
      if (story === 'upload') {
        await page.locator('input[type=file]').setInputFiles({ name: 'config.json', mimeType: 'application/json', buffer: Buffer.from('{}') });
        await expect(page.getByText('Uploaded', { exact: true })).toBeVisible();
        await page.locator('input[type=file]').setInputFiles({ name: 'invalid.txt', mimeType: 'text/plain', buffer: Buffer.from('x') });
        await expect(page.getByText('File type or size is not allowed')).toBeVisible();
      }
      if (story === 'time') {
        await expect(page.getByText('Last 15 minutes', { exact: true })).toBeVisible();
        await page.getByRole('button', { name: 'Refresh', exact: true }).click();
        await expect(page.getByLabel('Refresh count')).toHaveText('1');
        await expect(page.getByLabel('Selected time range')).toContainText('utc');
      }
      if (story === 'forms') {
        await page.getByRole('button', { name: 'Save name' }).click();
        await expect(page.getByRole('alert').filter({ hasText: 'A name is required' })).toBeVisible();
        await page.getByLabel('Name', { exact: true }).fill('hello');
        await page.getByRole('button', { name: 'Save name' }).click();
        await expect(page.getByLabel('Saved name')).toHaveText('hello');
        await page.getByLabel('Automatic save').fill('new value');
        await page.getByLabel('Description').focus();
        await expect(page.getByRole('button', { name: 'Retry', exact: true })).toBeVisible();
        await expect(page.getByLabel('Automatic save')).toHaveValue('new value');
        await page.getByRole('button', { name: 'Retry', exact: true }).click();
        await expect(page.getByLabel('Saved name')).toHaveText('new value');
        await page.getByRole('button', { name: 'First action' }).focus();
        await page.keyboard.press('ArrowRight');
        await expect(page.getByRole('button', { name: 'Second action' })).toBeFocused();
      }
      if (story === 'states') {
        await expect(page.getByLabel('Read-only token')).toHaveValue('***');
        await page.getByRole('combobox', { name: 'Empty choices', exact: true }).click();
        await expect(page.getByText('No matching options')).toBeVisible();
        await page.keyboard.press('Escape');
      }
      const result = await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
      expect(result.violations).toEqual([]);
      expect(failures).toEqual([]);
    });
  }
  test(`${theme}: schema forms`, async ({ page }) => {
    const failures: string[] = [];
    page.on('pageerror', (error) => failures.push(error.message));
    await page.goto(`/iframe.html?id=kit-schema-forms--editable&viewMode=story&globals=theme:${theme}`);
    await expect(page.getByLabel('Sequence', { exact: true })).toHaveValue('18446744073709551615');
    await expect(page.getByLabel('Capacity (MB)', { exact: true })).toHaveValue('256');
    await expect(page.getByLabel('Region', { exact: true })).toHaveCount(0);
    // An untouched form shows no messages; editing a field shows its own,
    // and a submit attempt summarizes every one without saving.
    await expect(page.getByRole('region', { name: 'Validation messages' })).toHaveCount(0);
    const name = page.getByLabel('Service name *', { exact: true });
    const original = await name.inputValue();
    await name.fill('');
    await expect(name).toHaveAttribute('aria-invalid', 'true');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.getByRole('region', { name: 'Validation messages' })).toBeVisible();
    await name.fill(original);
    await expect(name).toHaveAttribute('aria-invalid', 'false');
    await page.getByRole('checkbox', { name: 'Advanced', exact: true }).check();
    await expect(page.getByLabel('Region', { exact: true })).toBeVisible();
    await page.getByLabel('Replicas', { exact: true }).fill('6');
    await chooseOption(page, 'Region', { label: 'us' });
    await expect(page.getByLabel('Capacity (MB)', { exact: true })).toHaveValue('768');
    await chooseOption(page, 'Destination variant', 'queue');
    await expect(page.getByLabel('subject', { exact: true })).toHaveValue('events.hello');
    await expect(page.getByLabel('url', { exact: true })).toHaveCount(0);
    await page.getByRole('checkbox', { name: 'Simulate save failure' }).check();
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.getByRole('alert').filter({ hasText: 'Could not save' })).toBeVisible();
    await expect(page.getByLabel('Replicas', { exact: true })).toHaveValue('6');
    await page.getByRole('checkbox', { name: 'Simulate save failure' }).uncheck();
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    const preview = page.getByLabel('Masked values');
    await expect(preview).toContainText('18446744073709551615');
    await expect(preview).not.toContainText('fixture-secret');
    await expect(page.getByRole('button', { name: 'Reset', exact: true })).toBeDisabled();
    const result = await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
    expect(result.violations).toEqual([]);
    expect(failures).toEqual([]);
  });
  for (const story of ['actions', 'inputs', 'overlays', 'menus', 'navigation-and-layout', 'calendar']) {
    test(`${theme}: ${story}`, async ({ page }) => {
      const failures: string[] = [];
      page.on('pageerror', (error) => failures.push(error.message));
      await page.goto(`/iframe.html?id=kit-primitives--${story}&viewMode=story&globals=theme:${theme}`);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
      if (story === 'inputs') {
        await expect(page.getByRole('checkbox', { name: 'Enabled' })).not.toBeChecked();
        await expect(page.getByRole('radio', { name: 'Traces' })).toBeChecked();
      }
      if (story === 'overlays') {
        await expect(page.getByRole('button', { name: 'Remove fixture' })).toBeFocused();
        await page.getByRole('button', { name: 'Open details' }).click();
        await expect(page.getByRole('dialog')).toBeVisible();
        await page.keyboard.press('Escape');
        await expect(page.getByRole('button', { name: 'Open details' })).toBeFocused();
      }
      if (story === 'menus') {
        const trigger = page.getByRole('button', { name: 'Actions', exact: true });
        await trigger.focus();
        await page.keyboard.press('ArrowDown');
        await expect(page.getByRole('menuitem', { name: 'Inspect', exact: true })).toBeFocused();
        await page.keyboard.press('Escape');
        await expect(trigger).toBeFocused();
      }
      const result = await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
      expect(result.violations).toEqual([]);
      expect(failures).toEqual([]);
      if (story === 'inputs') await page.screenshot({ path: `test-results/kit-inputs-${theme}.png`, fullPage: true });
    });
  }
}
