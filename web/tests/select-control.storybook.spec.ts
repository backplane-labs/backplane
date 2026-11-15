import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import { chooseOption } from './choose-option';

for (const theme of ['dark', 'light'])
  test(`${theme}: shared select appearance and semantics`, async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`/iframe.html?id=kit-primitives--select-controls&viewMode=story&globals=theme:${theme}`);
    const trigger = page.getByRole('combobox', { name: 'Region', exact: true });
    await expect(trigger).toContainText('All regions');
    await expect(page.getByRole('combobox', { name: 'Disabled choice' })).toBeDisabled();
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations).toEqual(
      [],
    );
    await trigger.click();
    await expect(page.getByRole('option', { name: 'All regions', exact: true })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await expect(page.getByRole('option', { name: 'Unavailable region' })).toBeDisabled();
    expect(await page.getByRole('listbox').evaluate((element) => getComputedStyle(element).backgroundColor)).toBe(
      await page.locator('body').evaluate((element) => {
        const probe = document.createElement('div');
        probe.style.backgroundColor = 'var(--popover)';
        element.append(probe);
        const color = getComputedStyle(probe).backgroundColor;
        probe.remove();
        return color;
      }),
    );
    // Radix traps focus and hides the surrounding story while its popup is open.
    expect(
      (
        await new AxeBuilder({ page })
          .include('[data-slot=select-content]')
          .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
          .analyze()
      ).violations,
    ).toEqual([]);
    await page.keyboard.press('Escape');
    await expect(trigger).toBeFocused();
    await trigger.press('ArrowDown');
    await expect(page.getByRole('option', { name: 'All regions', exact: true })).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await expect(page.getByRole('option', { name: 'Europe', exact: true })).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page.getByLabel('Selected region')).toHaveText('eu');
    expect(await page.locator('form').evaluate((form) => new FormData(form as HTMLFormElement).get('region'))).toBe(
      'eu',
    );
    await chooseOption(page, 'Region', 'region-40');
    await expect(page.getByLabel('Selected region')).toHaveText('region-40');
    await chooseOption(page, 'Region', '');
    await expect(page.getByLabel('Selected region')).toHaveText('all');
    expect(await page.locator('form').evaluate((form) => new FormData(form as HTMLFormElement).get('region'))).toBe('');
    await page.setViewportSize({ width: 390, height: 844 });
    await trigger.click();
    expect(
      await page.getByRole('listbox').evaluate((element) => element.getBoundingClientRect().right),
    ).toBeLessThanOrEqual(390);
    await page.getByRole('option', { name: 'United States', exact: true }).click();
    await expect(page.getByLabel('Selected region')).toHaveText('us');
    expect(errors).toEqual([]);
  });
