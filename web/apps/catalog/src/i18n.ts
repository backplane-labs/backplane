import i18next from 'i18next';
import { initReactI18next } from 'react-i18next';
import { englishResources } from '@backplane-labs/backplane-ui';

export const i18n = i18next.createInstance();
await i18n.use(initReactI18next).init({
  lng: 'en', fallbackLng: 'en', supportedLngs: ['en'],
  interpolation: { escapeValue: false },
  resources: { en: {
    'backplane.ui': englishResources,
    translation: {
      title: 'Component compatibility',
      intro: 'Shared HyperDX tokens · shadcn controls · Grafana visualization',
      dark: 'Dark theme', light: 'Light theme',
      sources: 'Telemetry sources', source: 'Source', instance: 'Instance', state: 'State',
      healthy: 'Healthy', waiting: 'Waiting', rows: '{{count}} sources',
      filter: 'Filter sources', filterPlaceholder: 'Search service name',
      range: 'Time range', query: 'Query', queryLabel: 'LogsQL query',
      inspect: 'Inspect source', inspectTitle: 'Source details',
      inspectDescription: 'A shared dialog with editable local form state.',
      displayName: 'Display name', signal: 'Signal', logs: 'Logs', metrics: 'Metrics', traces: 'Traces',
      apply: 'Apply', applied: 'Applied: {{name}}', requests: 'Requests',
      precision: 'Sequence (lossless)', noSources: 'No matching sources',
      fixture: 'Local fixtures · no platform connection',
    },
  } },
});
