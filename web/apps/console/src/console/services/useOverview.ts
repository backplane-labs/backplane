import { CatalogServiceClient, GetServiceRequestSchema, type ServiceSummary } from '@backplane-labs/backplane-api';
import { usePlatformQuery } from '@backplane-labs/backplane-platform-ui';
import { useClient } from '@backplane-labs/backplane-react';
import { create } from '@bufbuild/protobuf';
import { useMemo } from 'react';
import { overview, type ServiceOverview } from './model';

/**
 * Per-service manifests and instance state for the landing. The catalog
 * index keys the fetch, so a catalog change refreshes it; one failing
 * service does not hide the others.
 */
export function useServicesOverview(services: ServiceSummary[], index: bigint | undefined) {
  const catalog = useClient(CatalogServiceClient);
  const names = services.map((service) => service.name).join(',');
  const details = usePlatformQuery(`overview:${index}:${names}`, (signal) =>
    Promise.allSettled(
      names
        ? names.split(',').map((name) => catalog.getService(create(GetServiceRequestSchema, { name }), { signal }))
        : [],
    ),
  );
  const items = useMemo<ServiceOverview[]>(
    () =>
      services.map((summary, position) => {
        const result = details.value?.[position];
        return overview(
          summary,
          result?.status === 'fulfilled' ? result.value : undefined,
          result?.status === 'rejected',
        );
      }),
    [services, details.value],
  );
  return { items };
}
