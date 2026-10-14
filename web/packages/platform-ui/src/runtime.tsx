import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { useBackplane, useConnection } from '@backplane-labs/backplane-react';
import { Button } from '@backplane-labs/backplane-ui';
import { usePlatformText } from './locales.js';

export function usePlatformQuery<T>(key: string, read: (signal: AbortSignal) => Promise<T>) {
  const owner = useBackplane(), connection = useConnection(), session = connection.session?.id ?? null;
  const latest = useRef(read); latest.current = read;
  const [refresh, setRefresh] = useState(0), [stored, setStored] = useState<{ owner: typeof owner; session: typeof session; key: string; connectionId?: typeof connection.connectionId; value?: T; error?: unknown; loading: boolean }>();
  useEffect(() => {
    const controller = new AbortController();
    setStored((old) => old?.owner === owner && old.session === session && old.key === key
      ? { ...old, loading: true } : { owner, session, key, loading: true });
    if (connection.connection === 'connected') void Promise.resolve().then(() => latest.current(controller.signal)).then((value) => {
      if (!controller.signal.aborted) setStored({ owner, session, key, connectionId: connection.connectionId, value, loading: false });
    }, (error) => { if (!controller.signal.aborted) setStored((old) => ({ ...old, owner, session, key, error, loading: false })); });
    return () => controller.abort();
  }, [owner, session, key, connection.connection, connection.connectionId, refresh]);
  const visible = stored?.owner === owner && stored.session === session && stored.key === key ? stored : undefined;
  return { value: visible?.value, error: visible?.error, loading: visible?.loading ?? true,
    stale: visible?.value !== undefined && (visible.error !== undefined || connection.connection !== 'connected' || visible.connectionId !== connection.connectionId), refresh: useCallback(() => setRefresh((value) => value + 1), []) };
}

/** A mutation runs once; callers must explicitly decide how to recover uncertain outcomes. */
export function usePlatformAction<T>() {
  const owner = useBackplane(), connection = useConnection(), session = connection.session?.id ?? null;
  const active = useRef<AbortController | null>(null);
  const [state, setState] = useState<{ owner: typeof owner; session: typeof session; pending: boolean; value?: T; error?: unknown }>();
  useEffect(() => () => { active.current?.abort(); active.current = null; }, [owner, session]);
  const run = async (execute: (signal: AbortSignal) => Promise<T>) => {
    if (active.current || connection.connection !== 'connected') return undefined;
    const controller = new AbortController(); active.current = controller; setState({ owner, session, pending: true });
    try {
      const value = await execute(controller.signal);
      if (controller.signal.aborted) return undefined;
      setState({ owner, session, pending: false, value }); return value;
    } catch (error) { if (!controller.signal.aborted) setState({ owner, session, pending: false, error }); return undefined; }
    finally { if (active.current === controller) active.current = null; }
  };
  const visible = state?.owner === owner && state.session === session ? state : undefined;
  return { pending: visible?.pending ?? false, value: visible?.value, error: visible?.error, run, disabled: connection.connection !== 'connected' };
}

export function ConnectionNotice() {
  const connection = useConnection(), text = usePlatformText();
  if (connection.connection === 'connected') return null;
  return <p role="status">{text(connection.connection === 'anonymous' ? 'anonymous' : connection.connection === 'offline' ? 'offline' : 'stale')}</p>;
}
export function QueryState({ state, children }: { state: { loading: boolean; stale?: boolean; error?: unknown; refresh: () => void }; children?: ReactNode }) {
  const text = usePlatformText();
  return <><ConnectionNotice />{state.loading && <p role="status">{text(state.stale ? 'stale' : 'loading')}</p>}
    {state.error !== undefined && <div role="alert">{text('error')} <Button type="button" variant="outline" onClick={state.refresh}>{text('retry')}</Button></div>}{children}</>;
}
export function MutationState({ action }: { action: { pending: boolean; error?: unknown } }) {
  const text = usePlatformText(); return <>{action.pending && <p role="status">{text('pending')}</p>}{action.error !== undefined && <p role="alert">{text('mutationFailed')}</p>}</>;
}
