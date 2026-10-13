import { lazy, Suspense, useState } from 'react';
import { Navigate, Route, Routes, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { CircleAlert, LoaderCircle, PlugZap, WifiOff } from 'lucide-react';
import { Button, EmptyState, ErrorBoundary, PageHeader, Skeleton, TooltipProvider } from '@gopherex/backplane-ui';
import { PluginProvider } from '@gopherex/backplane-plugin-sdk';
import { ExploreRoute } from './ExploreRoute';
import { WiringRoute } from './WiringRoute';
import { AuditRoute } from './AuditRoute';
import { ErrorsRoute } from './ErrorsRoute';
import { consoleErrorReporter } from './errors';
import type { ThemeMode } from '@gopherex/backplane-theme';
import Sidebar from './Sidebar';
import { PlatformProvider, FeatureGate } from './Platform';
import { InfrastructurePage } from './InfrastructurePage';
import type { useRegistry, ModuleEntry } from './registry';
import { Header } from './shell/Header';
import { NotFound } from './shell/NotFound';
import { ServicesPage } from './services/ServicesPage';
import { ServicePage } from './service/ServicePage';
import '@xyflow/react/dist/base.css';
import './tailwind.css';
import './console.css';

const Development = lazy(() => import('../dev'));

function ModulePage({ modules, mode, retry, loading }: { modules: Record<string, ModuleEntry>; mode: ThemeMode; retry: () => void; loading: boolean }) {
  const { service = '' } = useParams(), { t } = useTranslation('console'), entry = modules[service];
  const failure = (message: string, reset: () => void) => <div className="rounded-lg border border-border bg-card" role="alert">
    <EmptyState icon={<PlugZap />} title={t('failed')} description={message} action={<Button variant="outline" size="sm" onClick={reset}>{t('retry')}</Button>} />
  </div>;
  if (!entry) return loading ? <ModuleLoading /> : <div className="rounded-lg border border-border bg-card"><EmptyState icon={<PlugZap />} title={t('unavailable')} description={t('unavailableHint')} /></div>;
  if (entry.error) return failure(entry.error, retry);
  const plugin = entry.plugin;
  if (!plugin) return <ModuleLoading />;
  return <PluginProvider context={{ service, basePath: `/s/${service}`, mode, environment: 'embedded', reportError: consoleErrorReporter() }}>
    <ErrorBoundary resetKey={plugin} fallback={(error, reset) => failure(error.message, reset)}><plugin.Routes /></ErrorBoundary>
  </PluginProvider>;
}
function ModuleLoading() {
  const { t } = useTranslation('console');
  return <div role="status" aria-label={t('loading')} className="grid gap-3"><Skeleton className="h-8 w-64" /><Skeleton className="h-40" /><Skeleton className="h-40" /></div>;
}

export default function Console({ mode, onThemeChange, onLogout, registry }: { mode: ThemeMode; onThemeChange: (mode: ThemeMode) => void; onLogout: () => Promise<void>; registry: ReturnType<typeof useRegistry> }) {
  const { t } = useTranslation('console');
  const services = registry.catalog.value?.services ?? [], index = registry.catalog.value?.index;
  const [mobile, setMobile] = useState(false);
  const [logoutPending, setLogoutPending] = useState(false), [logoutFailed, setLogoutFailed] = useState(false);
  const logout = () => { setLogoutPending(true); setLogoutFailed(false); void onLogout().catch(() => setLogoutFailed(true)).finally(() => setLogoutPending(false)); };
  const toggleNavigation = () => { if (matchMedia('(max-width: 760px)').matches) setMobile((value) => !value); else document.querySelector<HTMLElement>('#console-navigation a')?.focus(); };
  return <PlatformProvider><TooltipProvider delayDuration={300}><div className="console-shell">
    <a className="console-skip" href="#console-content">{t('skip')}</a>
    <Header services={services} modules={registry.modules} mode={mode} onThemeChange={onThemeChange} onLogout={logout} logoutPending={logoutPending} navigationOpen={mobile} onNavigationToggle={toggleNavigation} />
    <div className="console-body">
      <Sidebar services={services} modules={registry.modules} mobile={mobile} onClose={() => setMobile(false)} />
      <main className="console-content" id="console-content" tabIndex={-1}>
        <div className="console-banners">
          {logoutFailed && <p className="console-banner" data-tone="danger" role="alert"><CircleAlert size={15} />{t('logoutFailed')}</p>}
          {registry.catalog.status === 'stale' && <p className="console-banner" data-tone="warning" role="status"><WifiOff size={15} />{t('stale')}</p>}
          {registry.catalog.status === 'error' && <p className="console-banner" data-tone="danger" role="alert"><CircleAlert size={15} />{t('catalogError')}</p>}
          {registry.descriptors.error !== undefined && <div className="console-banner" data-tone="danger" role="alert"><CircleAlert size={15} />{t('registryError')}<Button className="ml-auto" size="xs" variant="outline" onClick={registry.retry}>{t('retry')}</Button></div>}
        </div>
        <div className="console-page"><Routes>
          <Route path="/" element={<Navigate to="/services" replace />} />
          <Route path="/services" element={<ServicesPage services={services} index={index} loading={registry.catalog.status === 'loading'} mode={mode} />} />
          <Route path="/services/:service/:tab?" element={<ServicePage mode={mode} services={services} index={index} />} />
          <Route path="/wiring" element={<><PageHeader title={t('wiring')} description={t('wiringDescription')} /><div className="console-fill"><WiringRoute mode={mode} /></div></>} />
          <Route path="/explore" element={<><PageHeader title={t('explore')} description={t('exploreDescription')} /><div className="console-fill"><ExploreRoute mode={mode} /></div></>} />
          <Route path="/errors" element={<><PageHeader title={t('errors')} description={t('errorsDescription')} /><div className="console-fill"><FeatureGate feature="logs"><ErrorsRoute mode={mode} /></FeatureGate></div></>} />
          <Route path="/infrastructure" element={<InfrastructurePage />} />
          <Route path="/audit" element={<><PageHeader title={t('audit')} description={t('auditDescription')} /><div className="console-fill"><AuditRoute mode={mode} /></div></>} />
          <Route path="/s/:service/*" element={<div className="console-scroll"><ModulePage modules={registry.modules} mode={mode} retry={registry.retry} loading={registry.descriptors.loading} /></div>} />
          <Route path="/dev" element={<div className="console-scroll"><Suspense fallback={<p role="status"><LoaderCircle size={16} className="animate-spin" /></p>}><Development mode={mode} /></Suspense></div>} />
          <Route path="*" element={<NotFound />} />
        </Routes></div>
      </main>
    </div>
  </div></TooltipProvider></PlatformProvider>;
}
