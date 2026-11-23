import { createInstance } from 'i18next';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import { MemoryRouter } from 'react-router-dom';
import { expect, test } from 'vitest';
import { ModuleNavigation } from '../apps/console/src/console/ModuleNavigation';

const entries = [
  { path: 'settings', labelKey: 'settings', groupLabelKey: 'more' },
  { path: '', labelKey: 'overview' },
  { path: 'reports', labelKey: 'reports', groupLabelKey: 'more' },
];

async function render(path: string, lng: string) {
  const i18n = createInstance();
  await i18n.init({
    lng,
    resources: {
      en: { 'module.lic': { overview: 'Overview', settings: 'Settings', reports: 'Reports', more: 'More' } },
      ru: { 'module.lic': { overview: 'Обзор', settings: 'Настройки', reports: 'Отчёты', more: 'Ещё' } },
    },
  });
  return renderToStaticMarkup(
    createElement(
      I18nextProvider,
      { i18n },
      createElement(
        MemoryRouter,
        { initialEntries: [path] },
        createElement(ModuleNavigation, { service: 'lic', entries }),
      ),
    ),
  );
}

test('puts primary links first and keeps inactive groups closed', async () => {
  const html = await render('/s/lic/', 'en');
  expect(html).toContain('href="/s/lic/"');
  expect(html).toContain('aria-expanded="false"');
  expect(html).not.toContain('Settings');
  expect(html.indexOf('Overview')).toBeLessThan(html.indexOf('More'));
});

test('opens the localized group on deep links and preserves its entry order', async () => {
  const html = await render('/s/lic/reports/monthly', 'ru');
  expect(html).toContain('aria-expanded="true"');
  expect(html).toContain('Ещё');
  expect(html).toContain('aria-current="page"');
  expect(html).toContain('href="/s/lic/reports"');
  expect(html.indexOf('Настройки')).toBeLessThan(html.indexOf('Отчёты'));
  expect(html.match(/aria-expanded=/g)).toHaveLength(1);
});
