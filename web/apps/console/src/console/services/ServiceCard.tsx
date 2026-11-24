import {
  Duration,
  EntityCard,
  Meter,
  StatusBadge,
  StatusDot,
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@backplane-labs/backplane-ui';
import { AppWindow, Box, Clock, Settings2, TriangleAlert } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { healthLabel, healthTone, type ServiceOverview, transportTone } from './model';

export function ServiceCard({ item }: { item: ServiceOverview }) {
  const { t } = useTranslation('console'),
    { summary, config } = item;
  const tone = healthTone[summary.health];
  return (
    <EntityCard
      icon={<Box />}
      title={
        <Link className="console-card-link" to={`/services/${summary.name}`}>
          {summary.name}
        </Link>
      }
      subtitle={
        <span className="font-mono">
          {summary.latestVersion || '—'}
          {item.sdk && <> · sdk {item.sdk}</>}
        </span>
      }
      status={<StatusBadge tone={tone}>{t(healthLabel[summary.health])}</StatusBadge>}
      footer={
        <>
          <span className="inline-flex items-center gap-1" title={t('uptime')}>
            <Clock className="size-3.5" />
            {item.startedAt ? <Duration since={item.startedAt} /> : '—'}
          </span>
          <span className="ml-auto inline-flex items-center gap-3">
            <Link className="console-card-action" to={`/services/${summary.name}/configuration`}>
              <Settings2 className="size-3.5" />
              {t('configuration')}
            </Link>
            {summary.ui && (
              <Link className="console-card-action" to={`/s/${summary.name}`}>
                <AppWindow className="size-3.5" />
                {t('openModule')}
              </Link>
            )}
          </span>
        </>
      }
    >
      <div>
        <div className="mb-1.5 flex items-baseline justify-between text-xs">
          <span className="text-muted-foreground">{t('instances')}</span>
          <span className="font-mono tabular-nums">
            {summary.healthy}
            <span className="text-muted-foreground">
              {' '}
              / {summary.instances} {t('healthyShort')}
            </span>
          </span>
        </div>
        <Meter
          value={summary.healthy}
          max={summary.instances}
          tone={tone === 'neutral' ? 'danger' : tone}
          label={t('instances')}
        />
      </div>
      {(item.transports.length > 0 || config) && (
        <div className="flex flex-wrap items-center gap-1.5">
          {item.transports.map((transport) => (
            <Tooltip key={transport.name}>
              <TooltipTrigger asChild>
                {/* A button keeps the informational tooltip keyboard-reachable without a tabindex hack. */}
                <button type="button" className="console-chip">
                  <StatusDot tone={transportTone(transport)} className="size-1.5" />
                  {transport.name}
                </button>
              </TooltipTrigger>
              <TooltipContent>
                {t('transportState', { connected: transport.connected, total: transport.total })}
                {transport.error && ` — ${transport.error}`}
              </TooltipContent>
            </Tooltip>
          ))}
          {config &&
            (config.rejected ? (
              <Tooltip>
                <TooltipTrigger asChild>
                  <button type="button" className="console-chip console-chip-warning">
                    <TriangleAlert className="size-3" />
                    {t('configRejected', { revision: config.rejected.revision.toString() })}
                  </button>
                </TooltipTrigger>
                <TooltipContent>{config.rejected.error}</TooltipContent>
              </Tooltip>
            ) : (
              <span className="console-chip">{t('configApplied', { revision: config.applied.toString() })}</span>
            ))}
        </div>
      )}
      {item.failed && <p className="m-0 text-xs text-destructive">{t('detailFailed')}</p>}
    </EntityCard>
  );
}
