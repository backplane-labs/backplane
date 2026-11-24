import {
  Duration,
  Meter,
  StatusBadge,
  StatusDot,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@backplane-labs/backplane-ui';
import { AppWindow, Box } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { healthLabel, healthTone, type ServiceOverview, transportTone } from './model';

export function ServiceTable({ items }: { items: ServiceOverview[] }) {
  const { t } = useTranslation('console');
  return (
    <Table className="console-table">
      <TableHeader>
        <TableRow>
          <TableHead>{t('service')}</TableHead>
          <TableHead>{t('health')}</TableHead>
          <TableHead>{t('version')}</TableHead>
          <TableHead className="w-40">{t('instances')}</TableHead>
          <TableHead>{t('transports')}</TableHead>
          <TableHead>{t('config')}</TableHead>
          <TableHead>{t('uptime')}</TableHead>
          <TableHead>
            <span className="sr-only">{t('module')}</span>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {items.map((item) => {
          const { summary } = item,
            tone = healthTone[summary.health];
          return (
            <TableRow key={summary.name}>
              <TableCell>
                <Link className="console-row-link" to={`/services/${summary.name}`}>
                  <Box className="size-4 text-link" />
                  {summary.name}
                </Link>
              </TableCell>
              <TableCell>
                <StatusBadge tone={tone}>{t(healthLabel[summary.health])}</StatusBadge>
              </TableCell>
              <TableCell className="font-mono text-xs text-muted-foreground">{summary.latestVersion || '—'}</TableCell>
              <TableCell>
                <div className="flex items-center gap-2">
                  <span className="w-10 font-mono text-xs tabular-nums">
                    {summary.healthy}/{summary.instances}
                  </span>
                  <Meter
                    className="w-20"
                    value={summary.healthy}
                    max={summary.instances}
                    tone={tone === 'neutral' ? 'danger' : tone}
                    label={t('instances')}
                  />
                </div>
              </TableCell>
              <TableCell>
                <div className="flex gap-2">
                  {item.transports.map((transport) => (
                    <span
                      key={transport.name}
                      className="inline-flex items-center gap-1 text-xs text-muted-foreground"
                      title={transport.error}
                    >
                      <StatusDot tone={transportTone(transport)} className="size-1.5" />
                      {transport.name}
                    </span>
                  ))}
                </div>
              </TableCell>
              <TableCell className="text-xs">
                {item.config ? (
                  item.config.rejected ? (
                    <span className="text-warning" title={item.config.rejected.error}>
                      {t('configRejected', { revision: item.config.rejected.revision.toString() })}
                    </span>
                  ) : (
                    <span className="text-muted-foreground">
                      {t('configApplied', { revision: item.config.applied.toString() })}
                    </span>
                  )
                ) : (
                  '—'
                )}
              </TableCell>
              <TableCell className="text-xs text-muted-foreground">
                {item.startedAt ? <Duration since={item.startedAt} /> : '—'}
              </TableCell>
              <TableCell>
                {summary.ui && (
                  <Link
                    className="console-row-action"
                    to={`/s/${summary.name}`}
                    aria-label={t('openModuleOf', { service: summary.name })}
                    title={t('openModule')}
                  >
                    <AppWindow className="size-4" />
                  </Link>
                )}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
