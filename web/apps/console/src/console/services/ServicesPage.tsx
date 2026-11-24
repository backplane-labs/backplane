import { ServiceHealth, type ServiceSummary } from '@backplane-labs/backplane-api';
import { EmptyState, Input, PageHeader, Skeleton, StatRow, StatTile, ViewToggle } from '@backplane-labs/backplane-ui';
import { Activity, Boxes, LayoutGrid, Rows3, SearchX, Server } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ServiceCard } from './ServiceCard';
import { ServiceTable } from './ServiceTable';
import { useServicesOverview } from './useOverview';

type View = 'cards' | 'table';
const VIEW_KEY = 'backplane.services.view';
function readView(): View {
  try {
    const value = localStorage.getItem(VIEW_KEY);
    return value === 'table' ? value : 'cards';
  } catch {
    return 'cards';
  }
}

export function ServicesPage({
  services,
  index,
  loading,
}: {
  services: ServiceSummary[];
  index?: bigint;
  loading: boolean;
}) {
  const { t } = useTranslation('console');
  const [view, setView] = useState<View>(readView),
    [filter, setFilter] = useState('');
  useEffect(() => {
    try {
      localStorage.setItem(VIEW_KEY, view);
    } catch {
      /* Optional preference. */
    }
  }, [view]);
  const overview = useServicesOverview(services, index);
  const query = filter.trim().toLowerCase();
  const visible = overview.items.filter((item) => item.summary.name.toLowerCase().includes(query));
  const instances = services.reduce((sum, service) => sum + service.instances, 0),
    healthy = services.reduce((sum, service) => sum + service.healthy, 0);
  const unhealthy = services.filter(
    (service) => service.health === ServiceHealth.DEGRADED || service.health === ServiceHealth.DOWN,
  ).length;
  return (
    <>
      <PageHeader
        title={t('services')}
        description={t('servicesDescription')}
        actions={
          <>
            <Input
              className="console-filter"
              type="search"
              aria-label={t('filter')}
              placeholder={t('filter')}
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
            />
            <ViewToggle
              label={t('view')}
              value={view}
              onValueChange={setView}
              options={[
                { value: 'cards', label: t('cards'), icon: <LayoutGrid /> },
                { value: 'table', label: t('table'), icon: <Rows3 /> },
              ]}
            />
          </>
        }
      />
      <StatRow className="mb-5">
        <StatTile
          icon={<Boxes />}
          label={t('registered')}
          value={services.length}
          hint={t('withModules', { count: services.filter((service) => service.ui).length })}
        />
        <StatTile
          icon={<Server />}
          label={t('instances')}
          value={
            <>
              {healthy}
              <span className="text-base text-muted-foreground"> / {instances}</span>
            </>
          }
          hint={t('healthyInstances')}
          tone={healthy < instances ? 'warning' : 'success'}
        />
        <StatTile
          icon={<Activity />}
          label={t('attention')}
          value={unhealthy}
          hint={t('degradedOrDown')}
          tone={unhealthy ? 'danger' : 'neutral'}
        />
      </StatRow>
      <div className="console-fill console-services-body" data-view={view}>
        {loading && !services.length ? (
          <div className="console-card-grid">
            {[0, 1, 2].map((key) => (
              <Skeleton key={key} className="h-56 rounded-lg" />
            ))}
          </div>
        ) : !visible.length ? (
          <div className="rounded-lg border border-border bg-card">
            <EmptyState
              icon={<SearchX />}
              title={t(services.length ? 'empty' : 'noServices')}
              description={services.length ? t('emptyHint') : t('noServicesHint')}
            />
          </div>
        ) : view === 'cards' ? (
          <div className="console-card-grid">
            {visible.map((item) => (
              <ServiceCard key={item.summary.name} item={item} />
            ))}
          </div>
        ) : (
          <div className="flex min-h-0 flex-1 flex-col overflow-auto rounded-lg border border-border bg-card">
            <ServiceTable items={visible} />
          </div>
        )}
      </div>
    </>
  );
}
