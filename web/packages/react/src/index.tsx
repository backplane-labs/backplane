import { createContext, useContext, useEffect, useMemo, useState, useSyncExternalStore, type ReactNode } from 'react';
import { watchWithRetry, type ClientRuntime, type ClientConstructor, type ClientState } from '@backplane-labs/backplane-client';

const Context = createContext<ClientRuntime | null>(null);

/** The caller owns start/dispose; embedded modules never close the host client. */
export function BackplaneProvider({ client, children }: { client: ClientRuntime; children: ReactNode }) {
  return <Context.Provider value={client}>{children}</Context.Provider>;
}

export function useBackplane(): ClientRuntime {
  const client = useContext(Context);
  if (!client) throw new Error('BackplaneProvider is required');
  return client;
}

export function useConnection(): ClientState {
  const client = useBackplane();
  return useSyncExternalStore(client.subscribe, client.getSnapshot, client.getSnapshot);
}

export function useClient<T extends object>(constructor: ClientConstructor<T>): T {
  const client = useBackplane();
  return useMemo(() => client.client(constructor), [client, constructor]);
}

export interface WatchState<T> {
  readonly value?: T;
  readonly status: 'loading' | 'ready' | 'stale' | 'error';
  readonly error?: unknown;
}

/**
 * Only for streams whose first item replaces the complete snapshot (for example
 * WatchCatalog). Memoize `open`. Cursor/event streams need their own recovery.
 */
export function useSnapshotWatch<T>(open: (signal: AbortSignal) => AsyncIterable<T>): WatchState<T> {
  const client = useBackplane();
  const connection = useConnection();
  const [stored, setStored] = useState<{ owner: ClientRuntime; session: string | null; source: typeof open; state: WatchState<T> }>({
    owner: client, session: connection.session?.id ?? null, source: open, state: { status: 'loading' },
  });
  const session = connection.session?.id ?? null;
  useEffect(() => {
    const controller = new AbortController();
    const active = connection.connection === 'connected';
    setStored((previous) => {
      const sameSession = previous.owner === client && previous.session === session && previous.source === open;
      const value = sameSession ? previous.state.value : undefined;
      return { owner: client, session, source: open, state: { value, status: value === undefined ? 'loading' : 'stale' } };
    });
    if (active) {
      void (async () => {
        try {
          for await (const value of watchWithRetry(open, { signal: controller.signal,
            onRetry: (error) => { if (!controller.signal.aborted) setStored((previous) => ({ ...previous, state: { ...previous.state, status: 'stale', error } })); },
          })) {
            if (controller.signal.aborted) return;
            setStored({ owner: client, session, source: open, state: { value, status: 'ready' } });
          }
        } catch (error) {
          if (!controller.signal.aborted) setStored((previous) => ({ ...previous, state: { ...previous.state, status: 'error', error } }));
        }
      })();
    }
    return () => controller.abort();
  }, [client, session, connection.connection, connection.connectionId, open]);
  // Never display another session/provider's data, including the render before
  // the effect cleanup runs.
  if (stored.owner !== client || stored.session !== session || stored.source !== open) return { status: 'loading' };
  return stored.state;
}
