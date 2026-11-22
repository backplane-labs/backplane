import type { ValidationError } from '@gopherex/schemapb';
import { useTranslation } from 'react-i18next';
import { schemaFormsLocale } from './locales.js';
import { validationMessage } from './validation-locale.js';

export function useFormLocale() {
  const { t, i18n } = useTranslation('backplane.schemaForms');
  const { messages, russian } = schemaFormsLocale(i18n.language);
  return {
    t: (key: keyof typeof messages, args?: Record<string, string | number>) =>
      t(key, { defaultValue: messages[key], ...(russian ? { fallbackLng: false } : {}), ...args }),
    validation: (error: ValidationError) => validationMessage(error, russian),
  };
}
