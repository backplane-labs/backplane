import { useMemo, useState } from 'react';
import { create } from '@bufbuild/protobuf';
import { GetTraceRequestSchema, ObsServiceClient } from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { Badge, Button, EmptyState, Input, KeyValueList, Panel, Skeleton, StatusBadge, Timestamp } from '@backplane-labs/backplane-ui';
import { JSONViewer } from '@backplane-labs/backplane-editors';
import { attributeText, humanDuration, spanForest, type CorrelationTarget, type SpanNode, type SpanRecord } from '@backplane-labs/backplane-observability-ui';
import { ChevronDown, ChevronRight, CircleAlert, FileText, Minimize2, Maximize2, Search, Waypoints } from 'lucide-react';
import { traceSearchToList, tempoToSpans } from './telemetry-model.js';
import { usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { isoNanos } from './explore-logs.js';
import type { ExploreRun } from './explore.js';

const palette = ['#25e2a5', '#4dabf7', '#fcc419', '#b197fc', '#ff8787', '#63e6be', '#ffa94d', '#e599f7', '#a9e34b', '#74c0fc'];
export const serviceColor = (services: string[], service?: string) => palette[Math.max(0, services.indexOf(service ?? '')) % palette.length]!;

interface TraceRow { id: string; root: string; service: string; services: string[]; spans: number; errors: number; start: bigint; duration: bigint }

/** Spans stored as rows (VictoriaTraces LogsQL), grouped into traces. */
function groupRows(rows: Record<string, string>[]): TraceRow[] {
  const traces = new Map<string, Record<string, string>[]>();
  for (const row of rows) if (row.trace_id && row.name) traces.set(row.trace_id, [...traces.get(row.trace_id) ?? [], row]);
  return [...traces.entries()].map(([id, spans]) => {
    const start = (row: Record<string, string>) => BigInt(row.start_time_unix_nano || '0') || (isoNanos(row._time ?? '') ?? 0n);
    const end = (row: Record<string, string>) => BigInt(row.end_time_unix_nano || '0') || start(row) + BigInt(row.duration || '0');
    const root = spans.find((row) => !row.parent_span_id) ?? spans.reduce((a, b) => start(a) <= start(b) ? a : b);
    const from = spans.reduce((min, row) => start(row) < min ? start(row) : min, start(spans[0]!)), to = spans.reduce((max, row) => end(row) > max ? end(row) : max, 0n);
    return { id, root: root.name!, service: root['resource_attr:service.name'] ?? '', services: [...new Set(spans.map((row) => row['resource_attr:service.name'] ?? ''))],
      spans: spans.length, errors: spans.filter((row) => row.status_code === '2').length, start: from, duration: to > from ? to - from : 0n };
  }).sort((a, b) => a.start > b.start ? -1 : 1);
}

export function TraceResults({ run, onOpen }: { run: ExploreRun; mode: 'dark' | 'light'; onOpen: (traceId: string) => void }) {
  const text = usePlatformText();
  const traces = useMemo<TraceRow[]>(() => {
    const result = run.response.result;
    if (result.case === 'rows') return groupRows(result.value.rows.map((row) => row.fields));
    if (result.case === 'traces') return traceSearchToList(result.value).map((trace) => ({ id: trace.id, root: trace.name, service: trace.service ?? '', services: trace.service ? [trace.service] : [],
      spans: 0, errors: trace.error ? 1 : 0, start: BigInt(trace.startUnixNano || '0'), duration: BigInt(trace.durationNanos || '0') }));
    return [];
  }, [run]);
  const services = [...new Set(traces.flatMap((trace) => trace.services))].sort();
  const longest = traces.reduce((max, trace) => trace.duration > max ? trace.duration : max, 1n);
  return <Panel fill title={<><Waypoints className="size-4 text-muted-foreground" />{text('traces')}</>} count={traces.length} flush
    actions={<span className="flex flex-wrap gap-2 text-2xs text-muted-foreground">{services.slice(0, 8).map((service) => <span key={service} className="inline-flex items-center gap-1"><span className="size-2 rounded-sm" style={{ background: serviceColor(services, service) }} />{service || '—'}</span>)}</span>}>
    {!traces.length ? <EmptyState className="py-8" icon={<Waypoints />} title={text('noTraces')} /> : <table aria-label={text('traces')} className="w-full text-sm">
      <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-xs text-muted-foreground">{[text('rootSpan'), text('services'), text('spans'), text('startedAt'), text('duration')].map((label) => <th key={label} className="h-8 px-3 font-medium whitespace-nowrap">{label}</th>)}</tr></thead>
      <tbody>{traces.map((trace) => <tr key={trace.id} tabIndex={0} className="cursor-pointer border-b border-border last:border-b-0 hover:bg-raised focus-visible:bg-raised focus-visible:outline-none" onClick={() => onOpen(trace.id)} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); onOpen(trace.id); } }}>
        <td className="h-9 min-w-64 max-w-0 px-3"><span className="flex min-w-0 items-center gap-2">{trace.errors > 0 ? <CircleAlert className="size-3.5 shrink-0 text-destructive" /> : <span className="size-2 shrink-0 rounded-sm" style={{ background: serviceColor(services, trace.service) }} />}
          <span className="truncate font-mono text-xs">{trace.root}</span><span className="shrink-0 font-mono text-2xs text-muted-foreground">{trace.id.slice(0, 10)}</span></span></td>
        <td className="px-3"><span className="flex flex-wrap gap-1">{trace.services.map((service) => <Badge key={service} variant="outline" className="font-mono">{service || '—'}</Badge>)}</span></td>
        <td className="px-3 font-mono text-xs">{trace.spans || '—'}{trace.errors > 0 && <span className="ml-1 text-destructive">({trace.errors} err)</span>}</td>
        <td className="px-3 text-xs whitespace-nowrap text-muted-foreground"><Timestamp value={new Date(Number(trace.start / 1_000_000n))} /></td>
        <td className="w-56 px-3"><div className="flex items-center gap-2"><div className="h-1.5 flex-1 overflow-hidden rounded-full bg-raised"><div className={`h-full ${trace.errors ? 'bg-destructive/70' : 'bg-info/70'}`} style={{ width: `${Number(trace.duration * 100n / longest)}%` }} /></div>
          <span className="w-16 text-right font-mono text-xs">{humanDuration(trace.duration)}</span></div></td>
      </tr>)}</tbody>
    </table>}
  </Panel>;
}

export function TraceLookup({ traceId, mode, onNavigate }: { traceId: string; mode: 'dark' | 'light'; onNavigate?: (target: CorrelationTarget) => void }) {
  const client = useClient(ObsServiceClient), text = usePlatformText();
  const state = usePlatformQuery(`obs:trace:${traceId}`, async (signal) => { const response = await client.getTrace(create(GetTraceRequestSchema, { traceId }), { signal }); const raw = new TextDecoder().decode(response.tempoJson); return { response, raw, spans: tempoToSpans(raw) }; });
  if (!state.value) return state.error !== undefined ? <EmptyState icon={<CircleAlert />} title={text('error')} action={<Button size="sm" variant="outline" onClick={state.refresh}>{text('retry')}</Button>} /> : <Skeleton className="h-64" />;
  return <div className="grid gap-4"><TraceView spans={state.value.spans} traceId={traceId} onNavigate={onNavigate} />
    {(state.value.response.info?.partial || state.value.response.info?.truncated) && <p className="m-0 text-xs text-warning">{text('partial')}</p>}
    <details><summary className="cursor-pointer text-xs text-muted-foreground">{text('raw')}</summary><JSONViewer label={text('raw')} value={state.value.raw} mode={mode} /></details></div>;
}

const spanLength = (span: SpanRecord) => { try { return BigInt(span.endUnixNano) - BigInt(span.startUnixNano); } catch { return 0n; } };

/** Trace tree: collapsible spans on a shared time ruler, with span details beside it. */
export function TraceView({ spans, traceId, onNavigate }: { spans: readonly SpanRecord[]; traceId: string; onNavigate?: (target: CorrelationTarget) => void }) {
  const text = usePlatformText(), forest = useMemo(() => spanForest(spans), [spans]);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set()), [selected, setSelected] = useState<string>(), [filter, setFilter] = useState('');
  const services = useMemo(() => [...new Set(spans.map((span) => span.service ?? ''))].sort(), [spans]);
  const total = forest.start !== null && forest.end !== null && forest.end > forest.start ? forest.end - forest.start : 1n;
  const byStart = (a: SpanNode, b: SpanNode) => a.startUnixNano.localeCompare(b.startUnixNano, undefined, { numeric: true });
  const rows = useMemo(() => {
    const list: SpanNode[] = [], stack = [...forest.roots].sort(byStart).reverse();
    while (stack.length) { const node = stack.pop()!; list.push(node); if (!collapsed.has(node.id)) stack.push(...[...node.children].sort(byStart).reverse()); }
    return list;
  }, [forest, collapsed]);
  const matches = (span: SpanNode) => !filter || `${span.name} ${span.service} ${JSON.stringify(span.attributes ?? {})}`.toLowerCase().includes(filter.toLowerCase());
  const position = (span: SpanRecord) => { try { const from = BigInt(span.startUnixNano) - (forest.start ?? 0n); return { left: Number(from * 10000n / total) / 100, width: Math.max(0.2, Number(spanLength(span) * 10000n / total) / 100) }; } catch { return { left: 0, width: 0 }; } };
  const errors = spans.filter((span) => span.status === 'error').length;
  const span = spans.find((entry) => entry.id === selected) as SpanNode | undefined;
  const all = () => { const ids = new Set<string>(); const walk = (nodes: SpanNode[]) => nodes.forEach((node) => { if (node.children.length) ids.add(node.id); walk(node.children); }); walk(forest.roots); return ids; };
  return <div className="grid gap-3">
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
      <span><span className="text-foreground">{humanDuration(total)}</span> {text('total')}</span><span>{text('spansCount', { count: spans.length })}</span>
      <span>{text('servicesCount', { count: services.length })}</span>{errors > 0 && <span className="text-destructive">{text('errorsCount', { count: errors })}</span>}
      <span className="flex flex-wrap gap-2">{services.map((service) => <span key={service} className="inline-flex items-center gap-1"><span className="size-2 rounded-sm" style={{ background: serviceColor(services, service) }} />{service || '—'}</span>)}</span>
      <span className="ml-auto flex items-center gap-1">
        <span className="relative"><Search className="pointer-events-none absolute top-1.5 left-2 size-3.5" /><Input className="h-7 w-48 pl-7 text-xs" aria-label={text('findSpans')} placeholder={text('findSpans')} value={filter} onChange={(event) => setFilter(event.target.value)} /></span>
        <Button size="icon-sm" variant="ghost" aria-label={text('expandAll')} title={text('expandAll')} onClick={() => setCollapsed(new Set())}><Maximize2 /></Button>
        <Button size="icon-sm" variant="ghost" aria-label={text('collapseAll')} title={text('collapseAll')} onClick={() => setCollapsed(all())}><Minimize2 /></Button>
      </span>
    </div>
    {(forest.malformed || forest.invalidTime) && <p className="m-0 text-xs text-warning">{text(forest.malformed ? 'traceMalformed' : 'traceInvalidTime')}</p>}
    <div className="grid gap-3 2xl:grid-cols-[minmax(0,1fr)_380px]">
      <div className="overflow-hidden rounded-lg border border-border">
        <div className="grid grid-cols-[minmax(320px,2fr)_minmax(0,3fr)] border-b border-border bg-raised text-2xs text-muted-foreground">
          <span className="px-3 py-1.5">{text('span')}</span>
          <span className="relative h-full">{[0, 0.25, 0.5, 0.75, 1].map((tick) => <span key={tick} className="absolute top-1.5 px-1 font-mono whitespace-nowrap" style={{ left: `${tick * 100}%`, transform: `translateX(${tick === 0 ? 0 : tick === 1 ? -100 : -50}%)` }}>{humanDuration(BigInt(Math.round(Number(total) * tick)))}</span>)}</span>
        </div>
        <div className="max-h-[560px] overflow-auto" role="tree" aria-label={text('traceTree')}>
          {rows.map((node) => { const { left, width } = position(node), error = node.status === 'error', color = serviceColor(services, node.service), open = !collapsed.has(node.id);
            return <div key={node.id} role="treeitem" aria-level={node.depth + 1} aria-expanded={node.children.length ? open : undefined} aria-selected={selected === node.id} tabIndex={0}
              onClick={() => setSelected(node.id)} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); setSelected(node.id); } if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { event.preventDefault(); setCollapsed((old) => { const next = new Set(old); if (event.key === 'ArrowLeft') next.add(node.id); else next.delete(node.id); return next; }); } }}
              className={`grid cursor-pointer grid-cols-[minmax(320px,2fr)_minmax(0,3fr)] items-center border-b border-border text-xs last:border-b-0 hover:bg-raised focus-visible:bg-raised focus-visible:outline-none aria-selected:bg-primary/10 ${matches(node) ? '' : 'opacity-35'}`}>
              <span className="flex min-w-0 items-center gap-1 py-1 pr-2" style={{ paddingLeft: 6 + Math.min(node.depth, 20) * 14 }}>
                {node.children.length ? <button type="button" tabIndex={-1} aria-label={text(open ? 'collapse' : 'expand')} className="grid size-4 shrink-0 place-items-center rounded-sm text-muted-foreground hover:bg-accent"
                  onClick={(event) => { event.stopPropagation(); setCollapsed((old) => { const next = new Set(old); if (next.has(node.id)) next.delete(node.id); else next.add(node.id); return next; }); }}>{open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}</button> : <span className="size-4 shrink-0" />}
                <span className="h-3.5 w-1 shrink-0 rounded-full" style={{ background: color }} />
                {error && <CircleAlert className="size-3 shrink-0 text-destructive" />}
                <span className="truncate font-mono" title={node.name}>{node.name}</span>
                <span className="shrink-0 text-2xs text-muted-foreground">{node.service}</span>
                {!open && <Badge variant="outline" className="h-4 px-1 text-2xs">+{node.children.length}</Badge>}
              </span>
              <span className="relative h-6 border-l border-border">
                <span className="absolute top-1.5 h-3 rounded-sm" style={{ left: `${left}%`, width: `max(${width}%, 2px)`, background: error ? 'var(--destructive)' : color, opacity: 0.85 }} />
                <span className="absolute top-1 px-1 font-mono text-2xs text-muted-foreground" style={left + width > 75 ? { right: `calc(${100 - left}% + 2px)` } : { left: `calc(${left + width}% + 2px)` }}>{humanDuration(spanLength(node))}</span>
              </span>
            </div>; })}
        </div>
      </div>
      {span ? <SpanPanel span={span} services={services} traceStart={forest.start ?? 0n} onNavigate={onNavigate} traceId={traceId} /> : <Panel><EmptyState icon={<Waypoints />} title={text('selectSpan')} /></Panel>}
    </div>
  </div>;
}

function SpanPanel({ span, services, traceStart, onNavigate, traceId }: { span: SpanNode; services: string[]; traceStart: bigint; onNavigate?: (target: CorrelationTarget) => void; traceId: string }) {
  const text = usePlatformText(), offset = (() => { try { return BigInt(span.startUnixNano) - traceStart; } catch { return 0n; } })();
  const entries = (attributes?: Readonly<Record<string, unknown>>) => Object.entries(attributes ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const list = (items: [string, unknown][]) => <dl className="m-0 grid grid-cols-[minmax(110px,max-content)_minmax(0,1fr)] text-xs">{items.map(([key, value]) => <div key={key} className="contents">
    <dt className="border-b border-border px-3 py-1 font-mono text-muted-foreground">{key}</dt><dd className="m-0 border-b border-border px-3 py-1 font-mono break-all">{attributeText(value)}</dd></div>)}</dl>;
  return <Panel title={<span className="flex min-w-0 items-center gap-2"><span className="h-3.5 w-1 shrink-0 rounded-full" style={{ background: serviceColor(services, span.service) }} /><span className="truncate font-mono">{span.name}</span></span>} description={span.service} flush maxBodyHeight={560}
    actions={onNavigate && <Button size="xs" variant="outline" onClick={() => onNavigate({ signal: 'logs', traceId, spanId: span.id, resource: span.resource })}><FileText />{text('relatedLogs')}</Button>}>
    <div className="p-3"><KeyValueList items={[
      { label: text('status'), value: span.status === 'error' ? <StatusBadge tone="danger">{text('errorStatus')}</StatusBadge> : <StatusBadge tone="success">ok</StatusBadge> },
      { label: text('duration'), value: humanDuration(spanLength(span)), mono: true }, { label: text('offset'), value: `+${humanDuration(offset)}`, mono: true },
      { label: text('kind'), value: span.kind || '—', mono: true }, { label: text('spanId'), value: span.id, mono: true }, { label: text('parent'), value: span.parentId || '—', mono: true },
    ]} /></div>
    <div className="border-t border-border px-3 py-1.5 text-2xs font-semibold tracking-wider text-muted-foreground uppercase">{text('attributes')} · {entries(span.attributes).length}</div>
    {list(entries(span.attributes))}
    {span.resource && <><div className="border-t border-border px-3 py-1.5 text-2xs font-semibold tracking-wider text-muted-foreground uppercase">{text('resource')}</div>{list(entries(span.resource))}</>}
    {!!span.events?.length && <><div className="border-t border-border px-3 py-1.5 text-2xs font-semibold tracking-wider text-muted-foreground uppercase">{text('spanEvents')} · {span.events.length}</div>
      {span.events.map((event, index) => <details key={index} className="border-b border-border px-3 py-1.5 text-xs"><summary className="cursor-pointer font-mono">{event.name}</summary>{list(entries(event.attributes))}</details>)}</>}
    {!!span.links?.length && <><div className="border-t border-border px-3 py-1.5 text-2xs font-semibold tracking-wider text-muted-foreground uppercase">{text('links')}</div>
      {span.links.map((link, index) => <button key={index} type="button" className="block w-full truncate border-b border-border px-3 py-1.5 text-left font-mono text-xs text-link hover:underline" onClick={() => onNavigate?.({ signal: 'traces', traceId: link.traceId, spanId: link.spanId })}>{link.traceId} · {link.spanId}</button>)}</>}
  </Panel>;
}
