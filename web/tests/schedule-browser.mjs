import assert from 'node:assert/strict';
import { chromium, expect } from '@playwright/test';

const [base, token] = process.argv.slice(2);
assert.ok(base && token);
const browser = await chromium.launch();
try {
  const page = await browser.newPage();
  page.setDefaultTimeout(10000);
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.goto(new URL('services/hello/workflows', base).href);
  await page.getByLabel('Operator token', { exact: true }).fill(token);
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await page.getByRole('button', { name: 'Create schedule', exact: true }).click();
  const editor = page.getByRole('dialog');
  await editor.getByLabel('Schedule name', { exact: true }).fill('BrowserSchedule');
  await editor.getByRole('combobox', { name: 'Workflow', exact: true }).click();
  await page.getByRole('option', { name: 'Report', exact: true }).click();
  await editor.getByRole('combobox', { name: 'Timing', exact: true }).click();
  await page.getByRole('option', { name: 'Interval in seconds', exact: true }).click();
  await editor.getByLabel('Interval in seconds', { exact: true }).fill('2');
  await editor.getByRole('button', { name: 'Save schedule', exact: true }).click();
  await expect(editor).toHaveCount(0);
  // No manual trigger: the timer must start a real worker and produce a completed run.
  await expect(async () => {
    await page.reload();
    await expect(
      page.getByRole('row').filter({ hasText: 'hello/BrowserSchedule-' }).filter({ hasText: 'completed' }).first(),
    ).toBeVisible({ timeout: 3000 });
  }).toPass({ timeout: 45000, intervals: [2000] });
  const row = page.getByRole('group', { name: 'BrowserSchedule', exact: true });
  await row.getByRole('button', { name: 'Pause', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(row.getByText('Paused', { exact: true })).toBeVisible();
  await row.getByRole('button', { name: 'Edit schedule', exact: true }).click();
  await editor.getByRole('combobox', { name: 'Timing', exact: true }).click();
  await page.getByRole('option', { name: 'Interval in seconds', exact: true }).click();
  await editor.getByLabel('Interval in seconds', { exact: true }).fill('3600');
  await editor.getByRole('button', { name: 'Save schedule', exact: true }).click();
  await expect(editor).toHaveCount(0);
  await expect(row.getByText('Paused', { exact: true })).toBeVisible();
  await row.getByRole('button', { name: 'Delete schedule', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Confirm', exact: true }).click();
  await expect(row).toHaveCount(0);
  assert.deepEqual(errors, []);
  console.log('Schedule UI: create, timer execution completed, pause, edit preserving pause, delete passed');
} catch (error) {
  console.error(error);
  throw error;
} finally {
  await browser.close();
}
