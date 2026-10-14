import { Fragment } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { ChevronRight, Moon, PanelLeft, Sun } from 'lucide-react';
import { Button, StatusDot } from '@backplane-labs/backplane-ui';
import { useConnection } from '@backplane-labs/backplane-react';
import type { ServiceSummary } from '@backplane-labs/backplane-api';
import type { ThemeMode } from '@backplane-labs/backplane-theme';
import type { ModuleEntry } from '../registry';
import { isServiceTab } from '../service/tabs';
import { BrandMark } from './BrandMark';
import { CommandPalette } from './CommandPalette';
import { SessionMenu } from './SessionMenu';

type Crumb = { label: string; to?: string };

function useCrumbs(modules: Record<string, ModuleEntry>): Crumb[] {
  const { t, i18n } = useTranslation('console'), { pathname } = useLocation();
  const [root, service, ...rest] = pathname.split('/').filter(Boolean).map(decodeURIComponent);
  if (root === 'services' && service) {
    const tab = rest[0];
    return [{ label: t('services'), to: '/services' }, { label: service, to: tab ? `/services/${service}` : undefined }, ...(tab ? [{ label: isServiceTab(tab) ? t(tab) : tab }] : [])];
  }
  if (root === 's' && service) {
    const path = rest.join('/'), page = modules[service]?.plugin?.navigation.nav.find((entry) => entry.path === path);
    return [{ label: t('services'), to: '/services' }, { label: service, to: `/services/${service}` }, { label: page ? i18n.t(page.labelKey, { ns: `module.${service}` }) : path || t('module') }];
  }
  return [{ label: t(root === 'explore' || root === 'audit' || root === 'errors' || root === 'dev' || root === 'wiring' || root === 'infrastructure' ? root : 'services') }];
}

export function Header({ services, modules, mode, onThemeChange, onLogout, logoutPending, navigationOpen, onNavigationToggle }: {
  services: ServiceSummary[]; modules: Record<string, ModuleEntry>; mode: ThemeMode; onThemeChange: (mode: ThemeMode) => void;
  onLogout: () => void; logoutPending: boolean; navigationOpen: boolean; onNavigationToggle: () => void;
}) {
  const { t } = useTranslation('console'), connection = useConnection(), crumbs = useCrumbs(modules);
  const connected = connection.connection === 'connected';
  return <header className="console-header">
    <Button id="console-nav-toggle" className="console-nav-toggle" variant="ghost" size="icon" aria-label={t(navigationOpen ? 'close' : 'open')} aria-controls="console-navigation" aria-expanded={navigationOpen} onClick={onNavigationToggle}><PanelLeft size={17} /></Button>
    <Link to="/services" className="console-brand"><BrandMark />{t('brand')}</Link>
    <nav aria-label={t('breadcrumbs')} className="console-crumbs">
      <ol>{crumbs.map((crumb, index) => <Fragment key={index}>
        <li aria-hidden="true"><ChevronRight size={13} /></li>
        <li>{crumb.to ? <Link to={crumb.to}>{crumb.label}</Link> : <span aria-current={index === crumbs.length - 1 ? 'page' : undefined}>{crumb.label}</span>}</li>
      </Fragment>)}</ol>
    </nav>
    <div className="console-header-center"><CommandPalette services={services} modules={modules} mode={mode} onThemeChange={onThemeChange} /></div>
    <div className="console-header-actions">
      <span className="console-connection" role="status" title={t(connected ? 'connectionHelp' : 'disconnected', { state: connection.connection })}>
        <StatusDot tone={connected ? 'success' : 'warning'} pulse={!connected} /><span>{t(connected ? 'connected' : connection.connection === 'offline' ? 'disconnected' : 'reconnecting', { state: connection.connection })}</span>
      </span>
      <Button variant="ghost" size="icon" aria-label={t(mode === 'dark' ? 'light' : 'dark')} title={t(mode === 'dark' ? 'light' : 'dark')} onClick={() => onThemeChange(mode === 'dark' ? 'light' : 'dark')}>{mode === 'dark' ? <Sun size={16} /> : <Moon size={16} />}</Button>
      <SessionMenu onLogout={onLogout} pending={logoutPending} />
    </div>
  </header>;
}
