import { useEffect, useMemo, useRef, useState } from 'react';
import { create } from '@bufbuild/protobuf';
import { ListObsFieldValuesRequestSchema, ObsLanguage, ObsServiceClient, ObsSignal, QueryObsRequestSchema, type ObsSeries } from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { Badge, EmptyState, Input, Panel, Skeleton } from '@backplane-labs/backplane-ui';
import { TimeSeriesChart, formatValue } from '@backplane-labs/backplane-charts';
import { Activity, ChevronRight, LayoutDashboard, Search } from 'lucide-react';
import { usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { bucketLabels, metricUnit, nanosNow, ranges, stepFor, type ExploreRange, type MetricFunction } from './explore-query.js';

/** One panel of the generated dashboard: a metric family collapsed to one chart. */
interface Tile { id: string; metric: string; histogram: boolean; family: string }
type Section = { family: string; tiles: Tile[] };
const palette = ['#25e2a5', '#4dabf7', '#fcc419', '#ff8787', '#b197fc', '#63e6be', '#74c0fc', '#ffa94d', '#e599f7', '#a9e34b'];
const quote = (value: string) => JSON.stringify(value);

/** Groups metric names into families; histogram parts (_bucket/_count/_sum) become one tile. */
function sections(names: string[], service?: string): Section[] {
  const set = new Set(names), tiles: Tile[] = [];
  for (const name of [...set].sort()) {
    const base = name.replace(/_(bucket|count|sum)$/, '');
    if (name !== base && set.has(`${base}_bucket`)) { if (name.endsWith('_bucket')) tiles.push({ id: base, metric: base, histogram: true, family: '' }); continue; }
    if (name.endsWith('_bucket')) continue;
    tiles.push({ id: name, metric: name, histogram: false, family: '' });
  }
  for (const tile of tiles) tile.family = tile.metric.split(/[._]/)[0] ?? 'other';
  const order = (family: string) => family === service ? 0 : ['http', 'rpc'].includes(family) ? 1 : family === 'backplane' ? 2 : ['go', 'process', 'runtime', 'jvm'].includes(family) ? 4 : 3;
  const grouped = new Map<string, Tile[]>();
  for (const tile of tiles) grouped.set(tile.family, [...grouped.get(tile.family) ?? [], tile]);
  return [...grouped.entries()].map(([family, items]) => ({ family, tiles: items })).sort((a, b) => order(a.family) - order(b.family) || a.family.localeCompare(b.family));
}

// At most four panel queries in flight; the rest wait until they scroll into view and a slot frees.
let active = 0; const waiting: (() => void)[] = [];
async function slot<T>(work: () => Promise<T>): Promise<T> {
  if (active >= 4) await new Promise<void>((resolve) => waiting.push(resolve));
  active++;
  try { return await work(); } finally { active--; waiting.shift()?.(); }
}

const monotonic = (series: ObsSeries[]) => series.length > 0 && series.every((entry) => {
  const values = entry.samples.map((sample) => Number(sample.value)).filter(Number.isFinite);
  return values.length > 2 && values.every((value, index) => index === 0 || value >= values[index - 1]!) && values.at(-1)! > values[0]!;
});

/** Generated dashboard of every metric a service (or the installation) exports. */
export function MetricsOverview({ service, range, mode, onOpen }: { service?: string; range: ExploreRange; mode: 'dark' | 'light'; onOpen: (metric: string, fn: MetricFunction) => void }) {
  const client = useClient(ObsServiceClient), text = usePlatformText();
  const names = usePlatformQuery(`obs:overview:${service ?? ''}:${range}`, (signal) => {
    const to = nanosNow();
    return client.listObsFieldValues(create(ListObsFieldValuesRequestSchema, { signal: ObsSignal.METRICS, field: '__name__', limit: 1000,
      filter: service ? `{service.name=${quote(service)}}` : '', range: { startUnixNano: to - BigInt(ranges[range]) * 1_000_000n, endUnixNano: to } }), { signal });
  });
  const [search, setSearch] = useState(''), [closed, setClosed] = useState<Set<string>>(new Set());
  const groups = useMemo(() => sections(names.value?.values ?? [], service).map((section) => ({ ...section, tiles: section.tiles.filter((tile) => tile.metric.toLowerCase().includes(search.toLowerCase())) })).filter((section) => section.tiles.length), [names.value, service, search]);
  const count = groups.reduce((sum, section) => sum + section.tiles.length, 0);
  return <Panel fill flush title={<><LayoutDashboard className="size-4 text-muted-foreground" />{text(service ? 'serviceDashboard' : 'metricsDashboard', { service })}</>} count={count} description={text('dashboardHelp')}
    actions={<span className="relative"><Search className="pointer-events-none absolute top-2 left-2 size-3.5 text-muted-foreground" /><Input className="h-7 w-56 pl-7 text-xs" aria-label={text('filterMetrics')} placeholder={text('filterMetrics')} value={search} onChange={(event) => setSearch(event.target.value)} /></span>}>
    {names.loading && !names.value && <div className="grid grid-cols-3 gap-3 p-3">{[0, 1, 2, 3, 4, 5].map((key) => <Skeleton key={key} className="h-44" />)}</div>}
    {names.value && !count && <EmptyState className="py-10" icon={<Activity />} title={text('noMetrics')} />}
    {groups.map((section) => { const open = !closed.has(section.family); return <section key={section.family} className="border-b border-border last:border-b-0">
      <button type="button" aria-expanded={open} onClick={() => setClosed((old) => { const next = new Set(old); if (next.has(section.family)) next.delete(section.family); else next.add(section.family); return next; })}
        className="sticky top-0 z-[2] flex w-full items-center gap-2 bg-card px-3 py-2 text-left text-xs font-semibold tracking-wider text-muted-foreground uppercase hover:text-foreground">
        <ChevronRight className={`size-3.5 transition-transform ${open ? 'rotate-90' : ''}`} />{section.family}<Badge variant="outline" className="h-4 px-1 text-2xs normal-case">{section.tiles.length}</Badge>
      </button>
      {open && <div className="grid gap-3 px-3 pb-3 lg:grid-cols-2 2xl:grid-cols-3">{section.tiles.map((tile) => <MetricTile key={tile.id} tile={tile} service={service} range={range} mode={mode} onOpen={onOpen} />)}</div>}
    </section>; })}
  </Panel>;
}

function MetricTile({ tile, service, range, mode, onOpen }: { tile: Tile; service?: string; range: ExploreRange; mode: 'dark' | 'light'; onOpen: (metric: string, fn: MetricFunction) => void }) {
  const client = useClient(ObsServiceClient), text = usePlatformText(), host = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    const observer = new IntersectionObserver(([entry]) => { if (entry?.isIntersecting) { setVisible(true); observer.disconnect(); } }, { rootMargin: '200px' });
    if (host.current) observer.observe(host.current);
    return () => observer.disconnect();
  }, []);
  const selector = (metric: string) => `{__name__=${quote(metric)}${service ? `, service.name=${quote(service)}` : ''}}`;
  const by = service ? '' : ' by (service.name)';
  const state = usePlatformQuery(`obs:tile:${tile.id}:${service ?? ''}:${range}:${visible}`, async (signal) => {
    if (!visible) return undefined;
    const to = nanosNow(), request = (expression: string) => slot(() => client.queryObs(create(QueryObsRequestSchema, { signal: ObsSignal.METRICS, language: ObsLanguage.METRICSQL, expression,
      range: { startUnixNano: to - BigInt(ranges[range]) * 1_000_000n, endUnixNano: to }, stepNanos: stepFor(range, 120) }), { signal }));
    const series = (response: Awaited<ReturnType<typeof request>>) => response.result.case === 'timeSeries' ? response.result.value.series : [];
    if (tile.histogram) return { fn: 'p95' as MetricFunction, series: series(await request(`histogram_quantile(0.95, sum(rate(${selector(`${tile.metric}_bucket`)}[5m])) by (${bucketLabels.join(', ')}${service ? '' : ', service.name'}))`)) };
    const raw = series(await request(`sum(${selector(tile.metric)})${by}`));
    // A series that only grows is a counter: its rate is what an operator reads.
    return monotonic(raw) ? { fn: 'rate' as MetricFunction, series: series(await request(`sum(rate(${selector(tile.metric)}[5m]))${by}`)) } : { fn: 'sum' as MetricFunction, series: raw };
  });
  const chart = (state.value?.series ?? []).map((entry, index) => ({ id: String(index), label: entry.labels['service.name'] ?? tile.metric, color: palette[index % palette.length]!,
    points: entry.samples.map((sample) => ({ x: Number(sample.timestampSeconds) * 1000, value: sample.value })) }));
  const empty = !!state.value && !chart.length;
  // A quantile across services is the worst one, not their sum.
  const lasts = (state.value?.series ?? []).map((entry) => Number(entry.samples.at(-1)?.value) || 0);
  const last = !lasts.length ? undefined : tile.histogram ? Math.max(...lasts) : lasts.reduce((sum, value) => sum + value, 0);
  // A duration's quantile, or a duration gauge, reads in its unit; a rate of one does not.
  const unit = state.value?.fn === 'rate' ? undefined : metricUnit(tile.metric);
  return <div ref={host} className={`min-w-0 self-start rounded-md border border-border bg-background ${empty ? 'opacity-70' : ''}`}>
    <button type="button" onClick={() => onOpen(tile.histogram ? `${tile.metric}_bucket` : tile.metric, state.value?.fn ?? (tile.histogram ? 'p95' : 'raw'))} className="flex w-full min-w-0 items-center gap-2 border-b border-border px-2.5 py-1.5 text-left hover:bg-raised" title={text('openInExplore')}>
      <span className="min-w-0 flex-1 truncate font-mono text-xs" title={tile.metric}>{tile.metric}</span>
      {state.value && <Badge variant="outline" className="h-4 shrink-0 px-1 text-2xs">{text(`fn_${state.value.fn}`)}</Badge>}
      {last !== undefined && Number.isFinite(last) && <span className="shrink-0 font-mono text-xs text-foreground tabular-nums">{unit ? formatValue(last, unit) : Number(last.toPrecision(4))}</span>}
    </button>
    {empty ? <div className="px-2.5 py-2 text-2xs text-muted-foreground">{text('noSamples')}</div> : <div className="h-40 px-1 pt-1">
      {!state.value ? <Skeleton className="m-1 h-36" /> : <TimeSeriesChart label={tile.metric} series={chart} mode={mode} height={150} unit={unit} controls={false} timeZone={Intl.DateTimeFormat().resolvedOptions().timeZone} />}
    </div>}
  </div>;
}
