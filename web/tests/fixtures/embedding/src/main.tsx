import 'virtual:backplane-grafana-assets';
import { useEffect, useLayoutEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter, Routes, Route, Link } from 'react-router-dom';
import i18next from 'i18next';
import { initReactI18next, I18nextProvider, useTranslation } from 'react-i18next';
import { ThemeContext } from '@grafana/ui';
import { createGrafanaTheme, type ThemeMode } from '@backplane-labs/backplane-theme';
import { BackplaneProvider } from '@backplane-labs/backplane-react';
import { PluginProvider, installPluginTranslations } from '@backplane-labs/backplane-plugin-sdk';
import { loadPlugin, type LoadedPlugin } from '@backplane-labs/backplane-plugin-sdk/host';
import { Button, ErrorBoundary, englishResources } from '@backplane-labs/backplane-ui';
import { fixtureClient } from '../../../../templates/module/src/fixtures';
import '@backplane-labs/backplane-ui/style.css';
import '@backplane-labs/backplane-theme/style.css';
import '@fontsource/ibm-plex-sans/400.css';
import '@fontsource/ibm-plex-sans/500.css';
import '@fontsource/ibm-plex-mono/400.css';
import '../../../../templates/module/src/standalone.css';

const i18n = i18next.createInstance();
await i18n.use(initReactI18next).init({ lng: 'en', fallbackLng: 'en', supportedLngs: ['en'], interpolation: { escapeValue: false }, resources: { en: {
  'backplane.ui': englishResources,
  host: { title: 'Embedding fixture', home: 'Platform home', loading: 'Loading module', error: 'Module could not be loaded', light: 'Light theme', dark: 'Dark theme' },
} } });
function Host() {
  const { t } = useTranslation('host');
  const [mode, setMode] = useState<ThemeMode>('dark'), [plugin, setPlugin] = useState<LoadedPlugin>(), [error, setError] = useState<string>();
  useLayoutEffect(() => { document.documentElement.dataset.theme = mode; document.documentElement.classList.toggle('dark', mode === 'dark'); }, [mode]);
  useEffect(() => {
    const controller = new AbortController();
    void loadPlugin({ service: 'hello', hash: 'fixture-v1', sdkMajor: 0, path: '/backplane/plugins/hello/fixture-v1/' }, { consoleURL: `${location.origin}/backplane/`, signal: controller.signal }).then((loaded) => {
      if (controller.signal.aborted) return;
      installPluginTranslations(i18n, loaded.navigation); setPlugin(loaded);
    }, (reason: unknown) => { if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : String(reason)); });
    return () => controller.abort();
  }, []);
  return <ThemeContext.Provider value={createGrafanaTheme(mode)}>
    <header><nav><Link to="/">{t('home')}</Link>{plugin?.navigation.nav.map((item) => <Link key={item.path} to={`/s/hello/${item.path}`}>{i18n.t(item.labelKey, { ns: 'module.hello' })}</Link>)}</nav>
      <Button variant="outline" onClick={() => setMode(mode === 'dark' ? 'light' : 'dark')}>{t(mode === 'dark' ? 'light' : 'dark')}</Button></header>
    <main><Routes><Route path="/" element={<h1>{t('title')}</h1>} /><Route path="/s/hello/*" element={error ? <div role="alert">{t('error')}<p>{error}</p></div> : !plugin ? <p role="status">{t('loading')}</p> :
      <PluginProvider context={{ service: 'hello', basePath: '/s/hello', mode, environment: 'embedded' }}><ErrorBoundary resetKey={plugin} fallback={() => <p role="alert">{t('error')}</p>}><plugin.Routes /></ErrorBoundary></PluginProvider>} /></Routes></main>
  </ThemeContext.Provider>;
}
createRoot(document.getElementById('root')!).render(<BackplaneProvider client={fixtureClient}><I18nextProvider i18n={i18n}><BrowserRouter basename={import.meta.env.BASE_URL}><Host /></BrowserRouter></I18nextProvider></BackplaneProvider>);
