import { type GetServiceResponse, ServiceHealth, type ServiceSummary } from '@backplane-labs/backplane-api';
import { InstancePhase } from '@backplane-labs/backplane-api/backplanepb/v1/instance_pb';
import type { StatusTone } from '@backplane-labs/backplane-ui';
import { timestampDate } from '@bufbuild/protobuf/wkt';

export const healthTone: Record<ServiceHealth, StatusTone> = {
  [ServiceHealth.UNSPECIFIED]: 'neutral',
  [ServiceHealth.HEALTHY]: 'success',
  [ServiceHealth.DEGRADED]: 'warning',
  [ServiceHealth.DOWN]: 'danger',
};
export const healthLabel: Record<ServiceHealth, string> = {
  [ServiceHealth.UNSPECIFIED]: 'unknown',
  [ServiceHealth.HEALTHY]: 'healthy',
  [ServiceHealth.DEGRADED]: 'degraded',
  [ServiceHealth.DOWN]: 'down',
};
export const phaseTone: Record<InstancePhase, StatusTone> = {
  [InstancePhase.UNSPECIFIED]: 'neutral',
  [InstancePhase.STARTING]: 'info',
  [InstancePhase.SERVING]: 'success',
  [InstancePhase.STOPPING]: 'warning',
};

export type TransportHealth = { name: string; connected: number; total: number; error?: string };
export type ConfigHealth = { applied: bigint; rejected?: { revision: bigint; error: string } };

/** Everything the Services landing shows about one service. */
export interface ServiceOverview {
  summary: ServiceSummary;
  detail?: GetServiceResponse;
  failed: boolean;
  transports: TransportHealth[];
  config?: ConfigHealth;
  sdk?: string;
  startedAt?: Date;
}

export function overview(
  summary: ServiceSummary,
  detail: GetServiceResponse | undefined,
  failed: boolean,
): ServiceOverview {
  const manifest = detail?.latest,
    states = detail?.instances.flatMap((instance) => (instance.state ? [instance.state] : [])) ?? [];
  const transports = new Map<string, TransportHealth>();
  for (const state of states)
    for (const transport of state.transports) {
      const entry = transports.get(transport.name) ?? { name: transport.name, connected: 0, total: 0 };
      entry.total++;
      if (transport.connected) entry.connected++;
      else entry.error ??= transport.error;
      transports.set(transport.name, entry);
    }
  const rejected = states.find((state) => state.configRejectedRevision > 0n);
  const applied = states.reduce((max, state) => (state.configRevision > max ? state.configRevision : max), 0n);
  const started = states
    .flatMap((state) => (state.startedAt ? [timestampDate(state.startedAt)] : []))
    .sort((a, b) => a.getTime() - b.getTime())[0];
  return {
    summary,
    detail,
    failed,
    transports: [...transports.values()].sort((a, b) => a.name.localeCompare(b.name)),
    config: states.length
      ? {
          applied,
          rejected: rejected ? { revision: rejected.configRejectedRevision, error: rejected.configError } : undefined,
        }
      : undefined,
    sdk: manifest?.sdkVersion || states[0]?.sdkVersion || undefined,
    startedAt: started,
  };
}

export function transportTone(transport: TransportHealth): StatusTone {
  return transport.connected === transport.total ? 'success' : transport.connected ? 'warning' : 'danger';
}
