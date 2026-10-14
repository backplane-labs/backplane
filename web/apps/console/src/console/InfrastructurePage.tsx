import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { Activity, CircleAlert, Clock3, Database, Radio, RefreshCw, Server, SlidersHorizontal } from 'lucide-react';
import { Button, EmptyState, MetaItem, MetaList, PageHeader, Panel, Skeleton, StatRow, StatTile, StatusBadge, Table, TableBody, TableCell, TableHead, TableHeader, TableRow, Timestamp } from '@backplane-labs/backplane-ui';
import { usePlatformStatus } from './Platform';

const freshnessWindow = 15_000;

export function InfrastructurePage() {
  const { health, capabilities } = usePlatformStatus(), { t } = useTranslation('console');
  const [now, setNow] = useState(Date.now);
  useEffect(() => { const timer = setInterval(() => setNow(Date.now()), 1000); return () => clearInterval(timer); }, []);
  const entries = health.value?.dependencies ?? [];
  const checks = entries.map((entry) => {
    const checked = entry.checkedAt ? timestampDate(entry.checkedAt) : undefined;
    const stale = health.stale || !!checked && now - checked.getTime() > freshnessWindow;
    return { entry, checked, status: stale ? 'stale' : !checked ? 'checking' : entry.ok ? 'ok' : 'no' };
  });
  const count = (status: string) => checks.filter((check) => check.status === status).length;
  const ready = !!health.value, stale = count('stale'), unavailable = count('no');
  const features = capabilities.value?.capabilities ?? [];
  const overall = !ready ? 'checking' : stale ? 'infraStale' : unavailable ? 'degraded' : count('checking') || !checks.length ? 'checking' : 'healthy';
  const refresh = () => { health.refresh(); capabilities.refresh(); };
  return <>
    <PageHeader title={t('infrastructure')} description={t('infrastructureDescription')} actions={<Button variant="outline" onClick={refresh} disabled={health.loading || capabilities.loading}><RefreshCw className={health.loading ? 'size-3.5 animate-spin' : 'size-3.5'} />{t('refresh')}</Button>} />
    <div role="region" aria-label={t('infrastructure')} tabIndex={0} className="console-scroll grid content-start gap-4 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring">
      <MetaList>
        <MetaItem icon={<Radio />}><StatusBadge tone={overall === 'healthy' ? 'success' : overall === 'degraded' ? 'danger' : 'neutral'}>{t(overall)}</StatusBadge></MetaItem>
        <MetaItem icon={<Server />}>{health.value ? t('checkedBy', { instance: health.value.instanceId }) : t('loading')}</MetaItem>
        <MetaItem icon={<Clock3 />}>{t('infraRefreshCadence')}</MetaItem>
      </MetaList>
      {health.stale && <p role="status" className="console-banner" data-tone="warning"><CircleAlert className="size-4 shrink-0" />{t('infraConnectionStale')}</p>}
      {health.error !== undefined && <p role="alert" className="console-banner" data-tone="danger"><CircleAlert className="size-4 shrink-0" />{t('infrastructureError')}</p>}
      {!!stale && !health.stale && <p role="status" className="console-banner" data-tone="warning"><Clock3 className="size-4 shrink-0" />{t('infraChecksStale')}</p>}
      <StatRow>
        <StatTile icon={<Activity />} label={t('infraAvailable')} value={ready ? <>{count('ok')}<span className="text-base text-muted-foreground"> / {entries.length}</span></> : '—'} hint={t('infraAvailableHint')} tone={count('ok') ? 'success' : 'neutral'} />
        <StatTile icon={<CircleAlert />} label={t('infraUnavailable')} value={ready ? unavailable : '—'} hint={t('infraUnavailableHint')} tone={unavailable ? 'danger' : 'neutral'} />
        <StatTile icon={<Clock3 />} label={t('infraStale')} value={ready ? stale : '—'} hint={t('infraStaleHint')} tone={stale ? 'warning' : 'neutral'} />
        <StatTile icon={<SlidersHorizontal />} label={t('infraEnabledFeatures')} value={capabilities.value ? features.filter((feature) => feature.enabled).length : '—'} hint={t('infraEnabledFeaturesHint')} />
      </StatRow>
      <div className="grid items-start gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(280px,1fr)]">
        <Panel title={<><Database className="size-4 text-muted-foreground" />{t('infraDependencies')}</>} count={entries.length} flush footer={<span>{t('infraScopeHint')}</span>}>
          {!ready ? <div className="grid gap-3 p-3" role="status" aria-label={t('loading')}>{[0, 1, 2].map((key) => <Skeleton key={key} className="h-12" />)}</div> : !entries.length ? <EmptyState title={t('infraNoDependencies')} /> : <Table aria-label={t('infraDependencies')}>
            <TableHeader><TableRow><TableHead className="pl-3">{t('infraDependency')}</TableHead><TableHead>{t('health')}</TableHead><TableHead>{t('infraLastCheck')}</TableHead></TableRow></TableHeader>
            <TableBody>{checks.map(({ entry, checked, status }) => <TableRow key={entry.name}>
              <TableCell className="pl-3 whitespace-normal">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1"><span className="font-medium">{t(`infraNames.${entry.name}`, { defaultValue: entry.name })}</span><span className="text-2xs text-muted-foreground">{t(entry.required ? 'requiredDependency' : 'optionalDependency')}</span></div>
                <div className="mt-1 text-xs text-muted-foreground">{t(`infraChecks.${entry.name}`, { defaultValue: t('infraChecks.http') })}</div>
              </TableCell>
              <TableCell><StatusBadge tone={status === 'ok' ? 'success' : status === 'no' ? 'danger' : 'neutral'}>{status === 'ok' ? 'OK' : status === 'no' ? 'NO' : t(status === 'stale' ? 'infraStale' : 'checking')}</StatusBadge>
                {entry.reason && checked && <div className="mt-1 text-xs text-muted-foreground">{t(`infraReason.${entry.reason}`, { defaultValue: entry.reason })}</div>}
              </TableCell>
              <TableCell className="pr-3 text-xs text-muted-foreground">{checked ? <Timestamp value={checked} /> : '—'}</TableCell>
            </TableRow>)}</TableBody>
          </Table>}
        </Panel>
        <Panel title={<><SlidersHorizontal className="size-4 text-muted-foreground" />{t('deploymentFeatures')}</>} description={t('infraFeaturesHint')} flush>
          {capabilities.error !== undefined && <p role="alert" className="p-3 text-xs text-destructive">{t('capabilitiesError')}</p>}
          {capabilities.stale && <p role="status" className="px-3 py-2 text-xs text-warning">{t('infraFeaturesStale')}</p>}
          {!capabilities.value ? <div role="status" className="p-3">{t('loading')}</div> : <div className="divide-y divide-border">{features.map((feature) => <div key={feature.name} className="flex items-center justify-between gap-3 px-3 py-2.5 text-xs"><span>{t(`featureNames.${feature.name}`, { defaultValue: feature.name })}: </span>{' '}<StatusBadge tone="neutral">{t(feature.enabled ? 'enabledFeature' : 'disabledFeature')}</StatusBadge></div>)}</div>}
        </Panel>
      </div>
    </div>
  </>;
}
