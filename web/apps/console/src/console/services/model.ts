import { timestampDate } from '@bufbuild/protobuf/wkt';
import { InstancePhase } from '@backplane-labs/backplane-api/backplanepb/v1/instance_pb';
import { BindingState, ServiceHealth, type GetServiceResponse, type HookBinding, type Rule, type ServiceSummary } from '@backplane-labs/backplane-api';
import type { StatusTone } from '@backplane-labs/backplane-ui';

export const healthTone: Record<ServiceHealth, StatusTone> = {
  [ServiceHealth.UNSPECIFIED]: 'neutral', [ServiceHealth.HEALTHY]: 'success', [ServiceHealth.DEGRADED]: 'warning', [ServiceHealth.DOWN]: 'danger',
};
export const healthLabel: Record<ServiceHealth, string> = {
  [ServiceHealth.UNSPECIFIED]: 'unknown', [ServiceHealth.HEALTHY]: 'healthy', [ServiceHealth.DEGRADED]: 'degraded', [ServiceHealth.DOWN]: 'down',
};
export const phaseTone: Record<InstancePhase, StatusTone> = {
  [InstancePhase.UNSPECIFIED]: 'neutral', [InstancePhase.STARTING]: 'info', [InstancePhase.SERVING]: 'success', [InstancePhase.STOPPING]: 'warning',
};

export type TransportHealth = { name: string; connected: number; total: number; error?: string };
export type ConfigHealth = { applied: bigint; rejected?: { revision: bigint; error: string } };

/** Everything the Services landing shows about one service. */
export interface ServiceOverview {
  summary: ServiceSummary;
  detail?: GetServiceResponse;
  failed: boolean;
  contract: { apis: number; hooks: number; activities: number; events: number; subscriptions: number; workflows: number; schedules: number; nodes: number };
  transports: TransportHealth[];
  config?: ConfigHealth;
  sdk?: string;
  startedAt?: Date;
  bindings: { bound: number; total: number; missingRequired: number };
  rules: { active: number; paused: number };
}

export function overview(summary: ServiceSummary, detail: GetServiceResponse | undefined, failed: boolean, bindings: HookBinding[], rules: Rule[]): ServiceOverview {
  const manifest = detail?.latest, states = detail?.instances.flatMap((instance) => instance.state ? [instance.state] : []) ?? [];
  const transports = new Map<string, TransportHealth>();
  for (const state of states) for (const transport of state.transports) {
    const entry = transports.get(transport.name) ?? { name: transport.name, connected: 0, total: 0 };
    entry.total++; if (transport.connected) entry.connected++; else entry.error ??= transport.error;
    transports.set(transport.name, entry);
  }
  const rejected = states.find((state) => state.configRejectedRevision > 0n);
  const applied = states.reduce((max, state) => state.configRevision > max ? state.configRevision : max, 0n);
  const started = states.flatMap((state) => state.startedAt ? [timestampDate(state.startedAt)] : []).sort((a, b) => a.getTime() - b.getTime())[0];
  const own = bindings.filter((binding) => binding.service === summary.name);
  const ownRules = rules.filter((rule) => rule.current?.definition?.event.startsWith(`${summary.name}.`));
  return {
    summary, detail, failed,
    contract: {
      apis: manifest?.apis.length ?? 0, hooks: manifest?.hooks.length ?? 0, activities: manifest?.activities.length ?? 0,
      events: manifest?.events.length ?? 0, subscriptions: manifest?.subscriptions.length ?? 0, workflows: manifest?.workflows.length ?? 0,
      schedules: manifest?.schedules.length ?? 0, nodes: manifest?.nodes.length ?? 0,
    },
    transports: [...transports.values()].sort((a, b) => a.name.localeCompare(b.name)),
    config: states.length ? { applied, rejected: rejected ? { revision: rejected.configRejectedRevision, error: rejected.configError } : undefined } : undefined,
    sdk: manifest?.sdkVersion || states[0]?.sdkVersion || undefined,
    startedAt: started,
    bindings: {
      bound: own.filter((binding) => binding.state === BindingState.BOUND).length, total: own.length,
      missingRequired: own.filter((binding) => binding.state === BindingState.REQUIRED_UNBOUND).length,
    },
    rules: { active: ownRules.filter((rule) => !rule.paused).length, paused: ownRules.filter((rule) => rule.paused).length },
  };
}

export function transportTone(transport: TransportHealth): StatusTone {
  return transport.connected === transport.total ? 'success' : transport.connected ? 'warning' : 'danger';
}
