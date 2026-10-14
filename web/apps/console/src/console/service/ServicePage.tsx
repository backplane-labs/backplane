import { NavLink, Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { create } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { AppWindow, Box, Clock, Cpu, RefreshCw, Server, Tag } from 'lucide-react';
import { CatalogServiceClient, GetServiceRequestSchema, type ServiceSummary } from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { AutomationPanel, ConfigurationPanel, EventStreams, ServiceAPI, ServiceInspector, ServiceOperations, WorkflowsPanel, usePlatformQuery } from '@backplane-labs/backplane-platform-ui';
import { Button, Duration, EntityHeader, MetaItem, MetaList, StatusBadge, TabBar, TabItem } from '@backplane-labs/backplane-ui';
import type { ThemeMode } from '@backplane-labs/backplane-theme';
import { healthLabel, healthTone } from '../services/model';
import { AuditRoute } from '../AuditRoute';
import { ErrorsRoute } from '../ErrorsRoute';
import { isServiceTab, serviceTabs } from './tabs';
import { NotFound } from '../shell/NotFound';
import { ExploreRoute } from '../ExploreRoute';
import { FeatureGate } from '../Platform';
import { wiringLink } from '../WiringRoute';

export function ServicePage({ mode, services, index }: { mode: ThemeMode; services: ServiceSummary[]; index?: bigint }) {
  const { t } = useTranslation('console'), { service = '', tab = 'overview' } = useParams(), navigate = useNavigate();
  const [search, setSearch] = useSearchParams();
  const client = useClient(CatalogServiceClient), summary = services.find((entry) => entry.name === service);
  const detail = usePlatformQuery(`header:${service}:${index}`, (signal) => client.getService(create(GetServiceRequestSchema, { name: service }), { signal }));
  const started = detail.value?.instances.flatMap((instance) => instance.state?.startedAt ? [timestampDate(instance.state.startedAt)] : []).sort((a, b) => a.getTime() - b.getTime())[0];
  const sdk = detail.value?.latest?.sdkVersion;
  if (!isServiceTab(tab)) return <NotFound />;
  return <>
    <EntityHeader icon={<Box />} title={service}
      status={summary && <StatusBadge tone={healthTone[summary.health]}>{t(healthLabel[summary.health])}</StatusBadge>}
      meta={<MetaList>
        <MetaItem icon={<Tag />} label={t('version')} mono>{summary?.latestVersion || '—'}</MetaItem>
        <MetaItem icon={<Server />} label={t('instances')}>{summary ? t('instancesHealthy', { healthy: summary.healthy, total: summary.instances }) : '—'}</MetaItem>
        {sdk && <MetaItem icon={<Cpu />} label={t('sdk')} mono>sdk {sdk}</MetaItem>}
        {started && <MetaItem icon={<Clock />} label={t('uptime')}>{t('up')} <Duration since={started} /></MetaItem>}
      </MetaList>}
      actions={<>
        <Button variant="outline" size="sm" onClick={detail.refresh} disabled={detail.loading}><RefreshCw />{t('refresh')}</Button>
        {summary?.ui && <Button size="sm" asChild><Link to={`/s/${service}`}><AppWindow />{t('openModule')}</Link></Button>}
      </>}
      tabs={<TabBar aria-label={t('admin', { service })}>
        {serviceTabs.map((name) => <TabItem key={name} asChild><NavLink end to={`/services/${service}${name === 'overview' ? '' : `/${name}`}`}>{t(name)}</NavLink></TabItem>)}
      </TabBar>} />
    <div className="console-detail" key={`${service}/${tab}`}>
      {tab === 'overview' && <ServiceInspector service={service} mode={mode} />}
      {tab === 'api' && <ServiceAPI service={service} mode={mode} state={{ api: Number(search.get('api') ?? 0), operation: search.get('operation') ?? undefined }} onStateChange={(state) => {
        const next = new URLSearchParams(search); next.set('api', String(state.api));
        if (state.operation) next.set('operation', state.operation); else next.delete('operation');
        setSearch(next);
      }} />}
      {tab === 'configuration' && <ConfigurationPanel service={service} mode={mode} />}
      {tab === 'automation' && <AutomationPanel service={service} mode={mode} onOpenWiring={(target) => navigate(wiringLink(target))} />}
      {tab === 'operations' && <FeatureGate feature="workflows"><ServiceOperations service={service} mode={mode} /></FeatureGate>}
      {tab === 'events' && <FeatureGate feature="events"><EventStreams service={service} mode={mode} /></FeatureGate>}
      {tab === 'workflows' && <FeatureGate feature="workflows"><WorkflowsPanel service={service} mode={mode} /></FeatureGate>}
      {tab === 'telemetry' && <ExploreRoute service={service} mode={mode} defaultSignal="metrics" />}
      {tab === 'errors' && <FeatureGate feature="logs"><ErrorsRoute service={service} mode={mode} /></FeatureGate>}
      {tab === 'audit' && <AuditRoute service={service} mode={mode} />}
    </div>
  </>;
}
