import { useSearchParams } from 'react-router-dom';
import { decodeChips, defaultErrorsState, encodeChips, ErrorsExplorer, type ErrorsState } from '@gopherex/backplane-platform-ui';
import type { ThemeMode } from '@gopherex/backplane-theme';

const fields = new Set(['service', 'environment', 'type', 'message', 'release', 'trace_id', 'runtime_id', 'group_key', 'origin']);

/** Errors with their range, filters, text and refresh in the URL. */
export function ErrorsRoute({ mode, service }: { mode: ThemeMode; service?: string }) {
  const [params, setParams] = useSearchParams();
  const live = params.get('live');
  const state: ErrorsState = {
    range: { from: params.get('from') ?? defaultErrorsState.range.from, to: params.get('to') ?? defaultErrorsState.range.to, timeZone: params.get('tz') ?? defaultErrorsState.range.timeZone },
    chips: decodeChips(params.get('f'), (key) => fields.has(key)), text: params.get('q') ?? '', live: live === null ? defaultErrorsState.live : Number(live) || 0,
  };
  const change = (next: ErrorsState) => setParams((old) => {
    const result = new URLSearchParams(old);
    const set = (key: string, value: string, fallback: string) => { if (value && value !== fallback) result.set(key, value); else result.delete(key); };
    set('from', next.range.from, defaultErrorsState.range.from); set('to', next.range.to, defaultErrorsState.range.to);
    set('tz', next.range.timeZone, defaultErrorsState.range.timeZone); set('f', encodeChips(next.chips), ''); set('q', next.text, '');
    if (next.live === defaultErrorsState.live) result.delete('live'); else result.set('live', String(next.live));
    return result;
  }, { replace: true });
  return <ErrorsExplorer mode={mode} service={service} state={state} onStateChange={change} />;
}
