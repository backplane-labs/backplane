import { useSearchParams } from 'react-router-dom';
import { decodeChips, defaultErrorsState, encodeChips, ErrorsExplorer, errorsSorts, errorsStates, type ErrorsSort, type ErrorsState, type ErrorsStates } from '@backplane-labs/backplane-platform-ui';
import type { ThemeMode } from '@backplane-labs/backplane-theme';

const fields = new Set(['service', 'environment', 'type', 'message', 'release', 'trace_id', 'runtime_id', 'group_key', 'origin', 'fingerprint', 'handled', 'ingest', 'instance']);

/**
 * Errors with their range, filters, text, view, sort, refresh and open
 * drawers in the URL: `?fingerprint=<hex>&group_service=<service>` opens a
 * group, `?ref=<id>` an occurrence, so both can be linked; `?state=` picks
 * the triage states listed.
 */
export function ErrorsRoute({ mode, service }: { mode: ThemeMode; service?: string }) {
  const [params, setParams] = useSearchParams();
  const live = params.get('live'), fingerprint = params.get('fingerprint'), groupService = params.get('group_service'), sort = params.get('sort') as ErrorsSort | null, states = params.get('state') as ErrorsStates | null;
  const state: ErrorsState = {
    range: { from: params.get('from') ?? defaultErrorsState.range.from, to: params.get('to') ?? defaultErrorsState.range.to, timeZone: params.get('tz') ?? defaultErrorsState.range.timeZone },
    chips: decodeChips(params.get('f'), (key) => fields.has(key)), text: params.get('q') ?? '', live: live === null ? defaultErrorsState.live : Number(live) || 0,
    view: params.get('view') === 'occurrences' ? 'occurrences' : 'groups', sort: sort && errorsSorts.includes(sort) ? sort : defaultErrorsState.sort, states: states && errorsStates.includes(states) ? states : defaultErrorsState.states,
    group: fingerprint && groupService ? { fingerprint, service: groupService } : undefined, ref: params.get('ref') ?? undefined,
  };
  const change = (next: ErrorsState) => setParams((old) => {
    const result = new URLSearchParams(old);
    const set = (key: string, value: string, fallback: string) => { if (value && value !== fallback) result.set(key, value); else result.delete(key); };
    set('from', next.range.from, defaultErrorsState.range.from); set('to', next.range.to, defaultErrorsState.range.to);
    set('tz', next.range.timeZone, defaultErrorsState.range.timeZone); set('f', encodeChips(next.chips), ''); set('q', next.text, '');
    set('view', next.view, defaultErrorsState.view); set('sort', next.sort, defaultErrorsState.sort); set('state', next.states, defaultErrorsState.states);
    set('fingerprint', next.group?.fingerprint ?? '', ''); set('group_service', next.group?.service ?? '', ''); set('ref', next.ref ?? '', '');
    if (next.live === defaultErrorsState.live) result.delete('live'); else result.set('live', String(next.live));
    return result;
  }, { replace: true });
  return <ErrorsExplorer mode={mode} service={service} state={state} onStateChange={change} />;
}
