import { useMemo } from 'react';
import { create } from '@bufbuild/protobuf';
import { BindingServiceClient, CatalogServiceClient, GetServiceRequestSchema, ListBindingsRequestSchema, ListRulesRequestSchema, RuleServiceClient, type ServiceSummary } from '@gopherex/backplane-api';
import { useClient } from '@gopherex/backplane-react';
import { usePlatformQuery } from '@gopherex/backplane-platform-ui';
import { overview, type ServiceOverview } from './model';

/**
 * Per-service manifests and instance state for the landing. The catalog
 * index keys the fetch, so a catalog change refreshes it; one failing
 * service does not hide the others.
 */
export function useServicesOverview(services: ServiceSummary[], index: bigint | undefined) {
  const catalog = useClient(CatalogServiceClient), bindings = useClient(BindingServiceClient), rules = useClient(RuleServiceClient);
  const names = services.map((service) => service.name).join(',');
  const details = usePlatformQuery(`overview:${index}:${names}`, (signal) => Promise.allSettled(
    names ? names.split(',').map((name) => catalog.getService(create(GetServiceRequestSchema, { name }), { signal })) : []));
  const automation = usePlatformQuery(`automation:${index}`, (signal) => Promise.all([
    bindings.listBindings(create(ListBindingsRequestSchema), { signal }),
    rules.listRules(create(ListRulesRequestSchema), { signal }),
  ]));
  const items = useMemo<ServiceOverview[]>(() => services.map((summary, position) => {
    const result = details.value?.[position];
    return overview(summary, result?.status === 'fulfilled' ? result.value : undefined, result?.status === 'rejected',
      automation.value?.[0].bindings ?? [], automation.value?.[1].rules ?? []);
  }), [services, details.value, automation.value]);
  return { items, loading: details.loading && !details.value, automationLoaded: !!automation.value, refresh: () => { details.refresh(); automation.refresh(); } };
}
