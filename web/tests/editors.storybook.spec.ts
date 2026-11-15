import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import { chooseOption } from './choose-option';

for (const theme of ['dark', 'light']) {
  test(`${theme}: query editor completion cancellation and languages`, async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`/iframe.html?id=kit-editors--query&viewMode=story&globals=theme:${theme}`);
    const editor = page.getByRole('textbox', { name: 'Telemetry query', exact: true });
    await editor.fill('slow');
    await editor.press('Control+Space');
    await page.waitForTimeout(200);
    await editor.fill('fast');
    await editor.press('Control+Space');
    await expect(page.getByRole('option', { name: /fast.source/ })).toBeVisible();
    await page.waitForTimeout(100);
    await editor.press('Enter');
    await expect(editor).toContainText('fast.source');
    await expect(page.getByLabel('Cancelled suggestions')).not.toHaveText('0');
    await editor.press('Control+Enter');
    await expect(page.getByLabel('Query runs')).toHaveText('1');
    for (const language of ['traceql', 'cel', 'metricsql', 'promql', 'logsql']) {
      await chooseOption(page, 'Query language', language);
      await expect(editor).toContainText('fast.source');
    }
    await editor.fill('invalid');
    await expect(page.getByRole('list', { name: 'Editor diagnostics' })).toContainText('Unknown field: invalid');
    await editor.press('Escape');
    await editor.press('Tab');
    await expect(editor).not.toBeFocused();
    expect(
      (await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations,
    ).toEqual([]);
    expect(errors).toEqual([]);
  });
  test(`${theme}: JSON precision, validation and readonly diff`, async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`/iframe.html?id=kit-editors--json-and-diff&viewMode=story&globals=theme:${theme}`);
    const editor = page.getByRole('textbox', { name: 'JSON document', exact: true });
    await expect(editor).toContainText('18446744073709551615');
    await editor.fill('{invalid');
    await expect(page.getByRole('list', { name: 'Editor diagnostics' })).toBeVisible();
    await editor.fill('{"sequence":18446744073709551615}');
    await expect(page.getByLabel('Raw JSON')).toHaveText('{"sequence":18446744073709551615}');
    const original = page.getByRole('textbox', { name: 'Original value', exact: true });
    await expect(original).toContainText('18446744073709551615');
    await expect(original).toHaveAttribute('aria-readonly', 'true');
    await expect(page.getByRole('textbox', { name: 'Configuration diff: Before', exact: true })).toContainText('2');
    await expect(page.getByRole('textbox', { name: 'Configuration diff: After', exact: true })).toContainText('3');
    expect(
      (await new AxeBuilder({ page }).include('main').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations,
    ).toEqual([]);
    expect(errors).toEqual([]);
  });
}
