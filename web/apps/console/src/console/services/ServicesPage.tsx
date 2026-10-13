import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { SystemMap } from '@gopherex/backplane-platform-ui';
import type { ThemeMode } from '@gopherex/backplane-theme';
import { useTranslation } from 'react-i18next';
import { Activity, Boxes, Cable, LayoutGrid, Network, Rows3, Server, SearchX, Workflow, Zap } from 'lucide-react';
import { EmptyState, Input, PageHeader, Skeleton, StatRow, StatTile, ViewToggle } from '@gopherex/backplane-ui';
import { ServiceHealth, type ServiceSummary } from '@gopherex/backplane-api';
import { ServiceCard } from './ServiceCard';
import { ServiceTable } from './ServiceTable';
import { useServicesOverview } from './useOverview';
import { wiringLink } from '../WiringRoute';

type View = 'cards' | 'table' | 'map';
const VIEW_KEY = 'backplane.services.view';
function readView(): View { try { const value = localStorage.getItem(VIEW_KEY); return value === 'table' || value === 'map' ? value : 'cards'; } catch { return 'cards'; } }

export function ServicesPage({ services, index, loading, mode }: { services: ServiceSummary[]; index?: bigint; loading: boolean; mode: ThemeMode }) {
  const { t } = useTranslation('console'), navigate = useNavigate();
  const [view, setView] = useState<View>(readView), [filter, setFilter] = useState('');
  useEffect(() => { try { localStorage.setItem(VIEW_KEY, view); } catch { /* Optional preference. */ } }, [view]);
  const overview = useServicesOverview(services, index);
  const query = filter.trim().toLowerCase();
  const visible = overview.items.filter((item) => item.summary.name.toLowerCase().includes(query));
  const instances = services.reduce((sum, service) => sum + service.instances, 0), healthy = services.reduce((sum, service) => sum + service.healthy, 0);
  const unhealthy = services.filter((service) => service.health === ServiceHealth.DEGRADED || service.health === ServiceHealth.DOWN).length;
  const bindings = overview.items.reduce((sum, item) => ({ bound: sum.bound + item.bindings.bound, total: sum.total + item.bindings.total }), { bound: 0, total: 0 });
  const rules = overview.items.reduce((sum, item) => ({ active: sum.active + item.rules.active, paused: sum.paused + item.rules.paused }), { active: 0, paused: 0 });
  const automation = (value: string) => overview.automationLoaded ? value : '—';
  return <>
    <PageHeader title={t('services')} description={t('servicesDescription')} actions={<>
      <Input className="console-filter" type="search" aria-label={t('filter')} placeholder={t('filter')} value={filter} onChange={(event) => setFilter(event.target.value)} />
      <ViewToggle label={t('view')} value={view} onValueChange={setView} options={[
        { value: 'cards', label: t('cards'), icon: <LayoutGrid /> }, { value: 'table', label: t('table'), icon: <Rows3 /> }, { value: 'map', label: t('map'), icon: <Network /> },
      ]} />
    </>} />
    <StatRow className="mb-5">
      <StatTile icon={<Boxes />} label={t('registered')} value={services.length} hint={t('withModules', { count: services.filter((service) => service.ui).length })} />
      <StatTile icon={<Server />} label={t('instances')} value={<>{healthy}<span className="text-base text-muted-foreground"> / {instances}</span></>} hint={t('healthyInstances')} tone={healthy < instances ? 'warning' : 'success'} />
      <StatTile icon={<Activity />} label={t('attention')} value={unhealthy} hint={t('degradedOrDown')} tone={unhealthy ? 'danger' : 'neutral'} />
      <StatTile icon={<Cable />} label={t('bindings')} value={automation(`${bindings.bound}/${bindings.total}`)} hint={t('hooksBound')} />
      <StatTile icon={<Zap />} label={t('rules')} value={automation(String(rules.active))} hint={rules.paused ? t('pausedCount', { count: rules.paused }) : t('allActive')} />
      <StatTile icon={<Workflow />} label={t('workflows')} value={overview.loading ? '—' : overview.items.reduce((sum, item) => sum + item.contract.workflows, 0)} hint={t('declared')} />
    </StatRow>
    <div className="console-fill console-services-body" data-view={view}>
    {loading && !services.length ? <div className="console-card-grid">{[0, 1, 2].map((key) => <Skeleton key={key} className="h-56 rounded-lg" />)}</div>
      : !visible.length ? <div className="rounded-lg border border-border bg-card"><EmptyState icon={<SearchX />} title={t(services.length ? 'empty' : 'noServices')} description={services.length ? t('emptyHint') : t('noServicesHint')} /></div>
      : view === 'map' ? <SystemMap services={visible.map((item) => item.summary)} mode={mode} onOpenService={(name) => navigate(`/services/${name}`)} onOpenWiring={(target) => navigate(wiringLink(target))} />
      : view === 'cards' ? <div className="console-card-grid">{visible.map((item) => <ServiceCard key={item.summary.name} item={item} />)}</div>
      : <div className="flex min-h-0 flex-1 flex-col overflow-auto rounded-lg border border-border bg-card"><ServiceTable items={visible} /></div>}
    </div>
  </>;
}
