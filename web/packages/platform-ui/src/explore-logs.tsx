import { useEffect, useMemo, useRef, useState } from 'react';
import { create } from '@bufbuild/protobuf';
import { ObsLanguage, ObsServiceClient, ObsSignal, QueryObsRequestSchema } from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { Button, DetailDrawer, EmptyState, Input, Panel, StatusBadge, Switch, type StatusTone } from '@backplane-labs/backplane-ui';
import { JSONViewer } from '@backplane-labs/backplane-editors';
import { HighlightText, attributeText, type CorrelationTarget, type LogRecord } from '@backplane-labs/backplane-observability-ui';
import { FileText, History, Radio, Waypoints, WrapText } from 'lucide-react';
import { rowsToLogs } from './telemetry-model.js';
import { usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { nanosNow } from './explore-query.js';
import type { ExploreRun } from './explore.js';

export const severityTone = (value?: string): StatusTone => {
  const level = value?.toLowerCase() ?? '';
  return /fatal|crit|emerg|alert|err/.test(level) ? 'danger' : /warn/.test(level) ? 'warning' : /debug|trace/.test(level) ? 'neutral' : 'info';
};
const toneBar: Record<StatusTone, string> = { danger: 'bg-destructive', warning: 'bg-warning', info: 'bg-info/60', neutral: 'bg-subtle/60', success: 'bg-success', accent: 'bg-primary' };
const toneText: Record<StatusTone, string> = { danger: 'text-destructive', warning: 'text-warning', info: 'text-info', neutral: 'text-muted-foreground', success: 'text-success', accent: 'text-link' };

/** RFC 3339 with up to nanoseconds → unix nanoseconds. */
export function isoNanos(value: string): bigint | undefined {
  const match = /^(.*?)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/.exec(value), millis = match ? Date.parse(`${match[1]}${match[3]}`) : NaN;
  return Number.isNaN(millis) ? undefined : BigInt(millis) * 1_000_000n + BigInt((match![2] ?? '').padEnd(9, '0') || '0');
}
export function formatLogTime(value: string): string {
  const time = Date.parse(value); if (Number.isNaN(time)) return value;
  const date = new Date(time), fraction = /\.(\d{3})/.exec(value)?.[1] ?? String(date.getMilliseconds()).padStart(3, '0');
  return `${date.toLocaleDateString('en', { month: 'short', day: '2-digit' })} ${date.toLocaleTimeString('en', { hour12: false })}.${fraction}`;
}
const serviceOf = (record: LogRecord) => String(record.attributes?.['service.name'] ?? record.resource?.['service.name'] ?? '');

/** Log volume by level over the window (server-side LogsQL statistics). */
function LogVolume({ run }: { run: ExploreRun }) {
  const client = useClient(ObsServiceClient), text = usePlatformText(), buckets = 80;
  const step = (run.to - run.from) / BigInt(buckets);
  const state = usePlatformQuery(`obs:volume:${run.query}:${run.from}`, (signal) => client.queryObs(create(QueryObsRequestSchema, {
    signal: ObsSignal.LOGS, language: ObsLanguage.LOGSQL, expression: `${run.query} | stats by (severity_text) count() hits`, statistics: true,
    range: { startUnixNano: run.from, endUnixNano: run.to }, stepNanos: step > 1_000_000_000n ? step : 1_000_000_000n,
  }), { signal }));
  const bars = useMemo(() => {
    const result = Array.from({ length: buckets }, () => new Map<StatusTone, number>());
    const series = state.value?.result.case === 'timeSeries' ? state.value.result.value.series : [];
    const from = Number(run.from / 1_000_000n), span = Math.max(1, Number((run.to - run.from) / 1_000_000n));
    for (const entry of series) { const tone = severityTone(entry.labels.severity_text); for (const sample of entry.samples) {
      const index = Math.min(buckets - 1, Math.max(0, Math.floor((Number(sample.timestampSeconds) * 1000 - from) / span * buckets)));
      result[index]!.set(tone, (result[index]!.get(tone) ?? 0) + Number(sample.value));
    } }
    return result;
  }, [state.value, run]);
  const total = (bucket: Map<StatusTone, number>) => [...bucket.values()].reduce((sum, value) => sum + value, 0);
  const max = Math.max(1, ...bars.map(total)), count = bars.reduce((sum, bucket) => sum + total(bucket), 0);
  const order = ['danger', 'warning', 'info', 'neutral'] as const;
  return <div className="shrink-0 border-b border-border px-3 pt-2 pb-1">
    <div className="mb-1 flex items-center gap-3 text-2xs text-muted-foreground"><span>{text('logVolume')}</span><span className="font-mono">{text('matches', { count })}</span>
      <span className="ml-auto flex gap-2">{order.map((tone) => <span key={tone} className="inline-flex items-center gap-1"><span className={`size-2 rounded-sm ${toneBar[tone]}`} />{text(`tone_${tone}`)}</span>)}</span></div>
    <div className="flex h-14 items-end gap-px" role="img" aria-label={text('logVolume')}>
      {bars.map((bucket, index) => <div key={index} className="flex h-full flex-1 flex-col justify-end" title={String(total(bucket))}>
        {order.map((tone) => bucket.get(tone) ? <div key={tone} className={`w-full ${toneBar[tone]}`} style={{ height: `${bucket.get(tone)! / max * 100}%` }} /> : null)}
      </div>)}
    </div>
  </div>;
}

export function LogResults({ run, onNavigate, limit, mode }: { run: ExploreRun; onNavigate?: (target: CorrelationTarget) => void; limit: number; mode: 'dark' | 'light' }) {
  const client = useClient(ObsServiceClient), text = usePlatformText();
  const initial = useMemo(() => run.response.result.case === 'rows' ? rowsToLogs(run.response.result.value, crypto.randomUUID()) : [], [run]);
  const sort = (records: LogRecord[]) => [...records].sort((a, b) => (isoNanos(b.timestamp) ?? 0n) > (isoNanos(a.timestamp) ?? 0n) ? 1 : -1);
  const [records, setRecords] = useState(() => sort(initial)), [exhausted, setExhausted] = useState(initial.length < limit);
  useEffect(() => { setRecords(sort(initial)); setExhausted(initial.length < limit); }, [initial, limit]);
  const [search, setSearch] = useState(''), [selected, setSelected] = useState<LogRecord>(), [wrap, setWrap] = useState(true), [live, setLive] = useState(false), [loading, setLoading] = useState(false);
  const fetchRange = async (from: bigint, to: bigint, count: number) => {
    const response = await client.queryObs(create(QueryObsRequestSchema, { signal: ObsSignal.LOGS, language: ObsLanguage.LOGSQL, expression: run.query, range: { startUnixNano: from, endUnixNano: to }, limit: count }));
    return response.result.case === 'rows' ? rowsToLogs(response.result.value, crypto.randomUUID()) : [];
  };
  const older = async () => {
    const oldest = isoNanos(records.at(-1)?.timestamp ?? ''); if (!oldest) return;
    setLoading(true);
    try { const page = await fetchRange(run.from, oldest, limit); setRecords((old) => [...old, ...sort(page).filter((record) => (isoNanos(record.timestamp) ?? 0n) < oldest)]); setExhausted(page.length < limit); } finally { setLoading(false); }
  };
  const latest = useRef(records[0]?.timestamp); latest.current = records[0]?.timestamp;
  useEffect(() => {
    if (!live) return;
    const timer = setInterval(() => {
      const newest = isoNanos(latest.current ?? '') ?? run.from;
      void fetchRange(newest + 1n, nanosNow(), 500).then((page) => { if (page.length) setRecords((old) => [...sort(page), ...old].slice(0, 5000)); }, () => undefined);
    }, 5000);
    return () => clearInterval(timer);
  }, [live, run]); // eslint-disable-line react-hooks/exhaustive-deps -- the tail follows the current run
  const visible = search ? records.filter((record) => attributeText(record.body).toLowerCase().includes(search.toLowerCase())) : records;
  return <Panel fill title={<><FileText className="size-4 text-muted-foreground" />{text('logs')}</>} count={visible.length} flush
    actions={<>
      <Input className="h-7 w-48 text-xs" aria-label={text('highlight')} placeholder={text('highlight')} value={search} onChange={(event) => setSearch(event.target.value)} />
      <Button size="icon-sm" variant={wrap ? 'secondary' : 'ghost'} aria-pressed={wrap} aria-label={text('wrapLines')} title={text('wrapLines')} onClick={() => setWrap(!wrap)}><WrapText /></Button>
      <label className="ml-1 inline-flex items-center gap-1.5 text-xs text-muted-foreground"><Switch size="sm" checked={live} onCheckedChange={setLive} aria-label={text('liveTail')} /><Radio className={`size-3.5 ${live ? 'text-success' : ''}`} />{text('liveTail')}</label>
    </>}
    footer={<><span>{text('newestFirst')}</span><Button className="ml-auto" size="xs" variant="ghost" disabled={exhausted || loading} onClick={() => void older()}><History />{loading ? text('loading') : exhausted ? text('noOlder') : text('loadOlder')}</Button></>}>
    <div className="sticky top-0 z-[2] bg-card"><LogVolume run={run} /></div>
    {!visible.length ? <EmptyState className="py-8" icon={<FileText />} title={text('noLogs')} /> : <ol aria-label={text('logs')} className="m-0 list-none p-0 font-mono text-xs">
      {visible.map((record) => { const tone = severityTone(record.severity); return <li key={record.id}><button type="button" onClick={() => setSelected(record)}
        className="grid w-full grid-cols-[3px_150px_56px_110px_minmax(0,1fr)] items-start gap-2 border-b border-border py-1 pr-3 text-left hover:bg-raised">
        <span className={`h-full self-stretch ${toneBar[tone]}`} />
        <span className="whitespace-nowrap text-muted-foreground">{formatLogTime(record.timestamp)}</span>
        <span className={`uppercase ${toneText[tone]}`}>{record.severity || '—'}</span>
        <span className="truncate text-link" title={serviceOf(record)}>{serviceOf(record) || '—'}</span>
        <span className={`min-w-0 ${wrap ? 'break-words whitespace-pre-wrap' : 'truncate'}`}><HighlightText value={attributeText(record.body)} search={search} /></span>
      </button></li>; })}
    </ol>}
    <DetailDrawer open={!!selected} onOpenChange={(open) => { if (!open) setSelected(undefined); }} size="lg" title={text('logDetail')} description={selected?.timestamp}>
      {selected && <div className="grid gap-4">
        <div className="flex flex-wrap items-center gap-2"><StatusBadge tone={severityTone(selected.severity)}>{selected.severity || text('noSeverity')}</StatusBadge>
          {serviceOf(selected) && <span className="font-mono text-xs text-link">{serviceOf(selected)}</span>}
          {selected.traceId && <Button size="xs" variant="outline" onClick={() => { onNavigate?.({ signal: 'traces', traceId: selected.traceId, spanId: selected.spanId }); setSelected(undefined); }}><Waypoints />{text('openTrace')}</Button>}</div>
        <pre className="m-0 max-h-72 overflow-auto rounded-md border border-border bg-background p-3 font-mono text-xs whitespace-pre-wrap">{attributeText(selected.body)}</pre>
        <AttributeList title={text('attributes')} attributes={selected.attributes ?? {}} />
        <details><summary className="cursor-pointer text-xs text-muted-foreground">{text('raw')}</summary><JSONViewer label={text('raw')} value={selected.attributes ?? {}} mode={mode} /></details>
      </div>}
    </DetailDrawer>
  </Panel>;
}

export function AttributeList({ title, attributes }: { title: string; attributes: Readonly<Record<string, unknown>> }) {
  const entries = Object.entries(attributes).filter(([key]) => !key.startsWith('_stream')).sort(([a], [b]) => a.localeCompare(b));
  return <Panel title={title} count={entries.length} flush>
    <dl className="m-0 grid grid-cols-[minmax(120px,max-content)_minmax(0,1fr)] text-xs">{entries.map(([key, value]) => <div key={key} className="contents">
      <dt className="border-b border-border px-3 py-1 font-mono text-muted-foreground">{key}</dt><dd className="m-0 border-b border-border px-3 py-1 font-mono break-all">{attributeText(value)}</dd>
    </div>)}</dl>
  </Panel>;
}
