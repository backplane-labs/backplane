import { createContext, useContext, type ReactNode } from 'react';
import { EmptyState } from '@backplane-labs/backplane-ui';
import { usePlatformText } from './locales.js';

/** The host supplies deployment capabilities. Standalone fixtures may omit this provider. */
const ExecutionContext = createContext<{ bindings: boolean; rules: boolean } | undefined>(undefined);
export const PlatformExecutionProvider = ExecutionContext.Provider;
export function useExecutionEnabled(kind: 'binding' | 'rule') {
  const capabilities = useContext(ExecutionContext);
  return capabilities === undefined || capabilities[kind === 'binding' ? 'bindings' : 'rules'];
}
export function ExecutionGate({ kind, children }: { kind: 'binding' | 'rule'; children: ReactNode }) {
  const enabled = useExecutionEnabled(kind), text = usePlatformText();
  return enabled ? children : <EmptyState title={text('executionDisabled')} description={text('executionDisabledHelp')} />;
}
