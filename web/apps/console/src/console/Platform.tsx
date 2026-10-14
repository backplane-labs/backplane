import { PlatformExecutionProvider } from '@backplane-labs/backplane-platform-ui';
import { createContext, useContext, useEffect, type ReactNode } from 'react';
import { create } from '@bufbuild/protobuf';
import { PlatformServiceClient, GetCapabilitiesRequestSchema, GetInfrastructureRequestSchema } from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { usePlatformQuery } from '@backplane-labs/backplane-platform-ui';
import { Button, EmptyState } from '@backplane-labs/backplane-ui';
import { useTranslation } from 'react-i18next';

function usePlatformState() {
  const client = useClient(PlatformServiceClient);
  const capabilities = usePlatformQuery('platform:capabilities', (signal) => client.getCapabilities(create(GetCapabilitiesRequestSchema), { signal }));
  const health = usePlatformQuery('platform:health', (signal) => client.getInfrastructure(create(GetInfrastructureRequestSchema), { signal }));
  useEffect(() => { const timer = setInterval(health.refresh, 5000); return () => clearInterval(timer); }, [health.refresh]);
  useEffect(() => { const timer = setInterval(capabilities.refresh, 30000); return () => clearInterval(timer); }, [capabilities.refresh]);
  return { capabilities, health };
}
const Context = createContext<ReturnType<typeof usePlatformState> | null>(null);
export function PlatformProvider({ children }: { children: ReactNode }) {
  const state = usePlatformState();
  const enabled = (name: string) => state.capabilities.value?.capabilities.some((entry) => entry.name === name && entry.enabled) ?? false;
  return <Context.Provider value={state}><PlatformExecutionProvider value={{ bindings: enabled('bindings'), rules: enabled('rules') }}>{children}</PlatformExecutionProvider></Context.Provider>;
}
export function useCapabilities() { return useContext(Context)?.capabilities.value?.capabilities; }
export function usePlatformStatus() { return useContext(Context)!; }
const dependencies: Record<string, string[]> = { events: ['nats'], workflows: ['temporal'], schedules: ['temporal'], bindings: ['temporal'], rules: ['nats', 'temporal'], metrics: ['metrics'], logs: ['logs'], traces: ['traces'] };
export function FeatureGate({ feature, children }: { feature: string; children: ReactNode }) {
  const state = useContext(Context), { t } = useTranslation('console');
  if (!state) return children;
  if (!state.capabilities.value) return <EmptyState title={t(state.capabilities.error ? 'capabilitiesError' : 'loading')} action={state.capabilities.error ? <Button onClick={state.capabilities.refresh}>{t('retry')}</Button> : undefined} />;
  if (!state.capabilities.value.capabilities.some((capability) => capability.name === feature && capability.enabled)) return <EmptyState title={t('featureDisabled')} description={t('featureDisabledHint')} />;
  const unavailable = state.health.value?.dependencies.some((entry) => dependencies[feature]?.includes(entry.name) && entry.checkedAt && !entry.ok);
  return <>{unavailable && <p role="status" className="console-banner" data-tone="warning">{t('featureUnavailable')}</p>}{children}</>;
}
