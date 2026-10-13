import { useNavigate, useSearchParams } from 'react-router-dom';
import { AuditExplorer, decodeChips, defaultAuditState, encodeChips, type AuditState } from '@gopherex/backplane-platform-ui';
import type { ThemeMode } from '@gopherex/backplane-theme';

/** Audit with its range, filters, text and refresh in the URL; a record's trace opens in Explore. */
export function AuditRoute({ mode, service }: { mode: ThemeMode; service?: string }) {
  const [params, setParams] = useSearchParams(), navigate = useNavigate();
  const live = params.get('live');
  const state: AuditState = {
    range: { from: params.get('from') ?? defaultAuditState.range.from, to: params.get('to') ?? defaultAuditState.range.to, timeZone: params.get('tz') ?? defaultAuditState.range.timeZone },
    chips: decodeChips(params.get('f')), text: params.get('q') ?? '', live: live === null ? defaultAuditState.live : Number(live) || 0,
  };
  const change = (next: AuditState) => setParams((old) => {
    const result = new URLSearchParams(old);
    const set = (key: string, value: string, fallback: string) => { if (value && value !== fallback) result.set(key, value); else result.delete(key); };
    set('from', next.range.from, defaultAuditState.range.from); set('to', next.range.to, defaultAuditState.range.to);
    set('tz', next.range.timeZone, defaultAuditState.range.timeZone); set('f', encodeChips(next.chips), ''); set('q', next.text, '');
    if (next.live === defaultAuditState.live) result.delete('live'); else result.set('live', String(next.live));
    return result;
  }, { replace: true });
  return <AuditExplorer mode={mode} service={service} state={state} onStateChange={change}
    onOpenTrace={(trace) => navigate(`/explore?${new URLSearchParams({ signal: 'traces', range: '7d', trace })}`)} />;
}
