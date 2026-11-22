import { create } from '@bufbuild/protobuf';
import { ErrorCode, int32V, messageTemplates, SchemaSchema, ValidationErrorSchema } from '@gopherex/schemapb';
import { createInstance } from 'i18next';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import { expect, it } from 'vitest';
import { SchemaForm } from '../packages/schema-forms/src/form';
import { validationMessage } from '../packages/schema-forms/src/validation-locale';

const schema = create(SchemaSchema, {
  id: { name: 'registration' },
  fields: [{ name: 'nickname', title: 'Module label', kind: { case: 'string', value: {} } }],
});

it.each([
  ['en', 'Not set', 'Save'],
  ['ru', 'Не задано', 'Сохранить'],
  ['ru-RU', 'Не задано', 'Сохранить'],
])('renders SchemaForm for the host language %s', async (lng, absent, save) => {
  const i18n = createInstance();
  await i18n.init({
    lng,
    fallbackLng: 'en',
    resources: { en: { 'backplane.schemaForms': { absent: 'Not set' } } },
  });
  const markup = renderToStaticMarkup(
    createElement(
      I18nextProvider,
      { i18n },
      createElement(SchemaForm, { schema, label: 'Profile', onSubmit: () => {} }),
    ),
  );
  expect(markup).toContain(`placeholder="${absent}"`);
  expect(markup).toContain(`>${save}</button>`);
  expect(markup).toContain('Module label');
});

it.each([...messageTemplates.keys()])('provides a Russian template for validation code %s', (code) => {
  const error = create(ValidationErrorSchema, { code, message: 'fallback' });
  expect(validationMessage(error, true)).not.toBe('fallback');
});

it('formats typed validation arguments without changing the original message', () => {
  const error = create(ValidationErrorSchema, {
    code: ErrorCode.GTE_VIOLATED,
    expected: int32V(10),
    message: 'original',
  });
  expect(validationMessage(error, true)).toBe('Значение должно быть не меньше 10');
  expect(validationMessage(error, false)).toBe('original');
  expect(
    validationMessage(create(ValidationErrorSchema, { code: ErrorCode.RULE_VIOLATED, message: 'custom' }), true),
  ).toBe('custom');
});
