import { FeatureGate } from './Platform';
import { useSearchParams } from 'react-router-dom';
import { ExplorePanel, type ExploreRange, type ExploreSignal, type ExploreState } from '@gopherex/backplane-platform-ui';
import type { ThemeMode } from '@gopherex/backplane-theme';

const signals: ExploreSignal[] = ['logs', 'metrics', 'traces'];
const ranges: ExploreRange[] = ['5m', '15m', '1h', '3h', '6h', '12h', '24h', '7d'];

/** Explore with its query, signal, time range and open trace kept in the URL. */
export function ExploreRoute({ mode, service, defaultSignal = 'logs' }: { mode: ThemeMode; service?: string; defaultSignal?: ExploreSignal }) {
  const [params, setParams] = useSearchParams();
  const signal = params.get('signal'), range = params.get('range');
  const state: ExploreState = {
    signal: signals.includes(signal as ExploreSignal) ? signal as ExploreSignal : defaultSignal,
    range: ranges.includes(range as ExploreRange) ? range as ExploreRange : '15m',
    query: params.get('q') ?? undefined, language: params.get('lang') ?? undefined, trace: params.get('trace') ?? undefined,
    limit: Number(params.get('limit')) || undefined,
  };
  const change = (next: ExploreState) => setParams((old) => {
    const result = new URLSearchParams(old);
    const set = (key: string, value?: string) => { if (value) result.set(key, value); else result.delete(key); };
    set('signal', next.signal); set('range', next.range); set('q', next.query); set('lang', next.language); set('trace', next.trace); set('limit', next.limit ? String(next.limit) : undefined);
    return result;
  }, { replace: true });
  return <FeatureGate feature={state.signal}><ExplorePanel key={service} mode={mode} service={service} state={state} onStateChange={change}
    onNavigate={(target) => { if (target.signal === 'logs' && target.traceId) change({ ...state, signal: 'logs', language: 'logsql', query: `trace_id:=${JSON.stringify(target.traceId)}`, trace: undefined }); }} /></FeatureGate>;
}
