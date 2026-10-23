import 'virtual:backplane-grafana-assets';
import { lazy, Suspense, useEffect, useLayoutEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import i18next from 'i18next';
import { initReactI18next, I18nextProvider, useTranslation } from 'react-i18next';
import { ThemeContext } from '@grafana/ui';
import { createGrafanaTheme, type ThemeMode } from '@backplane-labs/backplane-theme';
import { BackplaneProvider, useConnection } from '@backplane-labs/backplane-react';
import { englishResources, Toaster } from '@backplane-labs/backplane-ui';
import { BackplaneClient } from '@backplane-labs/backplane-client';
import '@backplane-labs/backplane-ui/style.css';
import '@backplane-labs/backplane-theme/style.css';
import '@fontsource/ibm-plex-sans/400.css';
import '@fontsource/ibm-plex-sans/500.css';
import '@fontsource/ibm-plex-sans/600.css';
import '@fontsource/ibm-plex-mono/400.css';
import { consoleEnglish } from './console/locales';
import { Login } from './console/Login';
import { loginEnglish } from './console/login-locales';
import { consoleErrors } from './console/errors';

const Console = lazy(() => import('./console/ConsoleRuntime'));
const consoleBase = import.meta.env.BASE_URL;
const errors = consoleErrors(consoleBase);
const runtime = new BackplaneClient({ baseURL: new URL(consoleBase, location.origin).href });
const i18n = i18next.createInstance();
await i18n.use(initReactI18next).init({ lng: 'en', fallbackLng: 'en', supportedLngs: ['en'], interpolation: { escapeValue: false }, resources: { en: {
  'backplane.ui': englishResources,
  console: consoleEnglish,
  login: loginEnglish,
  host: { development: 'Development acceptance', developmentDescription: 'Platform compositions against the live installation.', services: 'Services', configuration: 'Configuration', operations: 'Operations', wiring: 'Wiring', automation: 'Automation', events: 'Events', runs: 'Runs', schedules: 'Schedules', audit: 'Audit', explore: 'Explore' },
} } });

function Host() {
  const { t } = useTranslation('console'), connection = useConnection();
  const [mode, setMode] = useState<ThemeMode>(() => { try { return localStorage.getItem('backplane.theme') === 'light' ? 'light' : 'dark'; } catch { return 'dark'; } });
  useLayoutEffect(() => { document.documentElement.dataset.theme = mode; document.documentElement.classList.toggle('dark', mode === 'dark'); }, [mode]);
  useEffect(() => { try { localStorage.setItem('backplane.theme', mode); } catch { /* Optional preference. */ } }, [mode]);
  if (!connection.session) return <Login client={runtime} state={connection.connection} mode={mode} onThemeChange={setMode} />;
  return <ThemeContext.Provider value={createGrafanaTheme(mode)}>
    <Suspense fallback={<p role="status">{t('loading')}</p>}><Console key={connection.session.id} mode={mode} onThemeChange={setMode} onLogout={() => runtime.logout()} /></Suspense>
    {/* One toast region for the shell and every plugin: the ui kit (and its toast store) is shared. */}
    <Toaster theme={mode} position="bottom-right" />
  </ThemeContext.Provider>;
}
void runtime.start();
createRoot(document.getElementById('root')!, { onCaughtError: errors.onReactError, onUncaughtError: errors.onReactError }).render(<BackplaneProvider client={runtime}><I18nextProvider i18n={i18n}><BrowserRouter basename={consoleBase}><Host /></BrowserRouter></I18nextProvider></BackplaneProvider>);
