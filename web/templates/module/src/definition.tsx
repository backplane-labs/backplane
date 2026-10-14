import { useEffect, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { create } from '@bufbuild/protobuf';
import { CatalogServiceClient, ListServicesRequestSchema, type ListServicesResponse } from '@backplane-labs/backplane-api';
import { AdminServiceClient } from './gen/proto/hello/console/v1/admin_ws_pb';
import { GetStatsRequestSchema } from './gen/proto/hello/console/v1/admin_pb';
import { usePlatformQuery } from '@backplane-labs/backplane-platform-ui';
import { useClient } from '@backplane-labs/backplane-react';
import { definePlugin, usePluginContext, usePluginNavigate } from '@backplane-labs/backplane-plugin-sdk';
import {
  Badge, Button, EmptyState, Field, FieldDescription, FieldLabel, Input, PageHeader, Panel, Skeleton, Stack, StatRow, StatTile,
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@backplane-labs/backplane-ui';
import navigation from './navigation';
import { lazy, Suspense } from 'react';
const Workbench = lazy(() => import('./workbench'));
function KitWorkbench() { const { t } = useTranslation('module.hello'); return <Suspense fallback={<Skeleton style={{ height: 256 }} role="status" aria-label={t('loading')} />}><Workbench /></Suspense>; }

// Module pages use only kit compositions, so they look native in the console
// and in the standalone shell without module-owned styles.
function Overview() {
  const { t } = useTranslation('module.hello');
  const context = usePluginContext();
  const navigate = usePluginNavigate();
  const api = useClient(CatalogServiceClient);
  const [services, setServices] = useState<ListServicesResponse>();
  const [error, setError] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    void api.listServices(create(ListServicesRequestSchema), { signal: controller.signal }).then(
      (response) => { if (!controller.signal.aborted) setServices(response); },
      () => { if (!controller.signal.aborted) setError(true); },
    );
    return () => controller.abort();
  }, [api]);
  return <Stack gap={16}>
    <PageHeader title={t('title')} description={t('description')} meta={<Badge variant="outline">{t(context.environment)}</Badge>}
      actions={<Button variant="outline" onClick={() => navigate('settings')}>{t('toSettings')}</Button>} />
    <ServiceStats />
    <Panel title={t('services')} count={services?.services.length} flush>
      {error ? <EmptyState title={t('error')} role="alert" /> : !services ? <Stack gap={8} style={{ padding: 12 }} role="status" aria-label={t('loading')}><Skeleton style={{ height: 28 }} /><Skeleton style={{ height: 28 }} /></Stack> :
        <Table><TableHeader><TableRow><TableHead>{t('service')}</TableHead><TableHead style={{ textAlign: 'right' }}>{t('instanceCount')}</TableHead></TableRow></TableHeader>
          <TableBody>{services.services.map((service) => <TableRow key={service.name}><TableCell style={{ fontFamily: 'var(--font-mono)' }}>{service.name}</TableCell><TableCell style={{ textAlign: 'right', fontVariantNumeric: 'tabular-nums' }}>{service.instances}</TableCell></TableRow>)}</TableBody>
        </Table>}
    </Panel>
  </Stack>;
}
function ServiceStats() {
  const { t } = useTranslation('module.hello'), client = useClient(AdminServiceClient);
  const state = usePlatformQuery('hello:stats', (signal) => client.getStats(create(GetStatsRequestSchema), { signal }));
  const value = (render: (stats: NonNullable<typeof state.value>) => ReactNode) => state.value ? render(state.value) : state.error !== undefined ? '—' : <Skeleton style={{ height: 28, width: 64 }} />;
  return <Panel title={t('stats')} actions={<Button size="sm" variant="ghost" onClick={state.refresh}>{t('refresh')}</Button>}
    footer={state.error !== undefined ? <span role="alert" style={{ color: 'var(--destructive)' }}>{t('statsError')}</span> : undefined}>
    <StatRow>
      <StatTile label={t('greetings')} value={value((stats) => <span data-testid="greetings">{String(stats.greetings)}</span>)} />
      <StatTile label={t('audited')} value={value((stats) => <span data-testid="audited">{String(stats.audited)}</span>)} />
      <StatTile label={t('greeting')} value={value((stats) => stats.currentGreeting)} tone="accent" />
    </StatRow>
  </Panel>;
}
function Settings() {
  const { t } = useTranslation('module.hello');
  const navigate = usePluginNavigate();
  const [name, setName] = useState('Hello');
  const [saved, setSaved] = useState('');
  return <Stack gap={16} style={{ maxWidth: 672 }}>
    <PageHeader title={t('settings')} description={t('settingsDescription')} actions={<Button variant="ghost" onClick={() => navigate('')}>{t('toOverview')}</Button>} />
    <Panel title={t('display')} footer={<span role="status">{saved ? t('saved', { name: saved }) : ''}</span>}>
      <form style={{ display: 'grid', gap: 16 }} onSubmit={(event) => { event.preventDefault(); setSaved(name); }}>
        <Field><FieldLabel htmlFor="name">{t('displayName')}</FieldLabel><Input id="name" value={name} onChange={(event) => setName(event.target.value)} required />
          <FieldDescription>{t('displayNameHelp')}</FieldDescription></Field>
        <div><Button type="submit">{t('save')}</Button></div>
      </form>
    </Panel>
  </Stack>;
}
function Missing() {
  const { t } = useTranslation('module.hello'), navigate = usePluginNavigate();
  return <Panel><EmptyState title={<h1 style={{ margin: 0, fontSize: 16, fontWeight: 500 }}>{t('missing')}</h1>} action={<Button size="sm" variant="outline" onClick={() => navigate('')}>{t('toOverview')}</Button>} /></Panel>;
}
export const definition = definePlugin({ ...navigation, routes: [
  { index: true, element: <Overview /> }, { path: 'settings', element: <Settings /> }, { path: 'workbench', element: <KitWorkbench /> }, { path: '*', element: <Missing /> },
] });
