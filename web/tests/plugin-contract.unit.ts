import { createInstance } from 'i18next';
import { expect, test } from 'vitest';
import { assertRelativePath, definePlugin, installPluginTranslations } from '../packages/plugin-sdk/src/index';

test.each(['/admin', '../other', 'nested/../../other', '%2Fadmin', '%2e%2e/escape', 'foo\\bar', 'foo?x=1', 'foo#bar'])(
  'rejects route escape %s',
  (path) => {
    expect(() => assertRelativePath(path)).toThrow();
  },
);
test('allows module-owned nested routes and rejects duplicate navigation targets', () => {
  const valid = {
    service: 'hello',
    english: {},
    nav: [{ path: '', labelKey: 'overview' }],
    routes: [{ path: 'traces/:traceId', children: [{ path: 'spans/:spanId' }] }],
  };
  expect(definePlugin(valid)).toBe(valid);
  expect(() => definePlugin({ ...valid, nav: [...valid.nav, ...valid.nav] })).toThrow('unique paths');
});
test('installs nested translation trees under the module namespace', async () => {
  const i18n = createInstance();
  await i18n.init({ lng: 'en', resources: {} });
  const namespace = installPluginTranslations(i18n, {
    service: 'hello',
    english: { title: 'Hello', sections: { greeter: { name: 'Greeter' } } },
  });
  expect(i18n.t('sections.greeter.name', { ns: namespace })).toBe('Greeter');
  expect(i18n.t('title', { ns: namespace })).toBe('Hello');
});
