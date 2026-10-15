import { useTranslation } from 'react-i18next';
export const editorsEnglish = {
  loading: 'Loading editor…', language: 'Query language', run: 'Run query', download: 'Download',
  before: 'Before', after: 'After', invalidJSON: 'Invalid JSON', diagnostics: 'Editor diagnostics',
  completionFailed: 'Suggestions are unavailable.', validationFailed: 'Validation is unavailable.',
  escapeTab: 'Press Escape, then Tab to leave the editor.',
  format: 'Format', formatHint: 'Pretty-print the JSON (Shift+Alt+F)',
} as const;
export function useEditorText() { const { t } = useTranslation('backplane.editors'); return (key: keyof typeof editorsEnglish) => t(key, { defaultValue: editorsEnglish[key] }); }
