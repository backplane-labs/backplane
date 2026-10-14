import { useCallback, useMemo, useState } from 'react';
import { create, type JsonValue } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { ErrorFacetsRequestSchema, ErrorHistogramRequestSchema, ErrorOrigin, ErrorRelation, ErrorServiceClient, GetErrorRequestSchema, RelatedLogsRequestSchema,
  SearchErrorsRequestSchema, type ErrorOccurrence, type GetErrorResponse } from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { Badge, Button, DetailDrawer, EmptyState, SelectControl, Panel, Skeleton, Timestamp, resolveTimeRange, type TimeRangeValue } from '@backplane-labs/backplane-ui';
import { JSONViewer } from '@backplane-labs/backplane-editors';
import { ErrorDetails, LogViewer } from '@backplane-labs/backplane-observability-ui';
import { AppWindow, Bug, CircleAlert, Server, Waypoints } from 'lucide-react';
import { usePlatformText } from './locales.js';
import { usePlatformQuery } from './runtime.js';
import { serviceColor } from './wiring/graph.js';
import { TraceLookup } from './explore-traces.js';
import { FilterRow } from './audit.js';
import { addValue, FeedFacets, FeedFilterBar, FeedHistogram, FeedTimeBar, textOps, useFresh, valueLabel, type FeedChip, type FeedOp, type FeedTarget } from './feed.js';
import { causesOf, errorFieldEnum, errorFieldNames, errorFieldOf, errorFilterOf, historyOf, relatedRecords, stateOf, type ErrorFieldName } from './errors-model.js';

/** What the errors page shows; the host keeps it in the URL. */
export interface ErrorsState { range: TimeRangeValue; chips: FeedChip[]; text: string; /** Refresh interval, ms; 0: off. */ live: number }
export const defaultErrorsState: ErrorsState = { range: { from: 'now-24h', to: 'now', timeZone: 'browser' }, chips: [], text: '', live: 10_000 };

const facetFields: readonly ErrorFieldName[] = ['service', 'type', 'environment', 'release'];
const originOps: readonly FeedOp[] = ['is', 'is_not'];

/**
 * Errors from the log store: exceptions the browser SDK captures and any
 * OpenTelemetry exception log (the Go SDK's CaptureError and recovered
 * panics), one table narrowed by field conditions, text and a time range,
 * with a histogram, value counts, a live refresh and a drawer holding the
 * stack, the SDK's state and history, the trace and related logs.
 * `service` pins a service condition.
 */
export function ErrorsExplorer({ mode, service, state, onStateChange }: {
  mode: 'dark' | 'light'; service?: string; state: ErrorsState; onStateChange: (state: ErrorsState) => void;
}) {
  const text = usePlatformText(), client = useClient(ErrorServiceClient);
  const [tick, setTick] = useState(0), [pages, setPages] = useState<string[]>([]), [selected, setSelected] = useState<string>();
  const chips = useMemo(() => service ? [{ key: 'service', op: 'is' as const, values: [service] }, ...state.chips] : state.chips, [service, state.chips]);
  const range = useMemo(() => resolveTimeRange(state.range), [state.range, tick]); // eslint-disable-line react-hooks/exhaustive-deps
  const filter = useMemo(() => range && errorFilterOf(chips, state.text, range), [chips, state.text, range]);
  const key = JSON.stringify([chips, state.text, state.range]);
  const setChips = (next: FeedChip[]) => { setPages([]); onStateChange({ ...state, chips: next }); };
  const add = (field: string, value: JsonValue, exclude = false) => setChips(addValue(state.chips, field, value, exclude));

  const feed = usePlatformQuery(`errors-feed:${key}:${tick}:${pages.join(',')}`, async (signal) => {
    const occurrences: ErrorOccurrence[] = [];
    let next = '', partial = false;
    for (const cursor of ['', ...pages]) {
      const page = await client.searchErrors(create(SearchErrorsRequestSchema, { filter, pageSize: 100, pageCursor: cursor }), { signal });
      occurrences.push(...page.occurrences); next = page.nextPageCursor; partial ||= page.partial;
    }
    return { occurrences, next, partial };
  });
  const histogram = usePlatformQuery(`errors-histogram:${key}:${tick}`, (signal) => client.errorHistogram(create(ErrorHistogramRequestSchema, { filter }), { signal }));
  const facets = usePlatformQuery(`errors-facets:${key}:${tick}`, (signal) =>
    client.errorFacets(create(ErrorFacetsRequestSchema, { filter, fields: facetFields.map((field) => errorFieldEnum[field]), limit: 8 }), { signal }));
  const facetValues = useMemo(() => (facets.value?.facets ?? []).map((facet) => {
    const field = errorFieldOf(facet.field);
    return { key: field, label: text(`errorField_${field}`), values: facet.values.map((entry) => ({ value: entry.value as JsonValue, count: Number(entry.count) })) };
  }), [facets.value, text]);
  const targets = useMemo<FeedTarget[]>(() => errorFieldNames.map((field) => ({ key: field, label: text(`errorField_${field}`), ops: field === 'origin' ? originOps : textOps })), [text]);

  const occurrences = feed.value?.occurrences ?? [];
  const fresh = useFresh(useMemo(() => feed.value?.occurrences.map((occurrence) => occurrence.ref), [feed.value]), key);
  const refresh = useCallback(() => setTick((value) => value + 1), []);
  const occurrence = occurrences.find((item) => item.ref === selected);
  const time = (bucket: { start?: Parameters<typeof timestampDate>[0] }) => bucket.start ? timestampDate(bucket.start).getTime() : 0;

  return <section aria-label={text('errorsTitle')} className="flex h-full min-h-0 flex-col gap-3">
    <FeedFilterBar chips={state.chips} targets={targets} onChips={setChips} text={state.text} onText={(value) => onStateChange({ ...state, text: value })}
      suggest={(field) => field === 'origin' ? ['sdk', 'otel-log'] : facetValues.find((facet) => facet.key === field)?.values.map((entry) => entry.value) ?? []}
      pinned={service && <span className="inline-flex h-7 items-center rounded-md border border-border bg-raised px-2 font-mono text-xs">{text('serviceScope', { service })}</span>} />
    <FeedTimeBar range={state.range} onRange={(value) => onStateChange({ ...state, range: value })} live={state.live} onLive={(live) => onStateChange({ ...state, live })} onRefresh={refresh} mode={mode} />
    {!range && <p role="alert" className="m-0 text-xs text-destructive">{text('invalidTimeRange')}</p>}
    <FeedHistogram title={text('errorsOverTime')} mode={mode} loading={histogram.loading} count={histogram.value ? Number(histogram.value.total) : undefined}
      series={histogram.value && [{ id: 'errors', label: text('errorsTitle'), color: 'var(--destructive)', points: histogram.value.buckets.map((bucket) => ({ x: time(bucket), value: bucket.count })) }]}
      onRange={(from, to) => onStateChange({ ...state, range: { ...state.range, from: new Date(from).toISOString(), to: new Date(to).toISOString() } })} />
    <div className="grid min-h-0 flex-1 grid-cols-[240px_minmax(0,1fr)] gap-3">
      <FeedFacets facets={facetValues} chips={chips} onAdd={add} decorate={(field, value) => field === 'service'
        ? { prefix: <span className="relative size-1.5 shrink-0 rounded-full" style={{ background: serviceColor(valueLabel(value)) }} /> } : undefined} />
      <Panel fill flush title={<><Bug className="size-4 text-muted-foreground" />{text('occurrences')}</>} count={occurrences.length}
        footer={<><span>{feed.value?.partial ? text('partial') : text('newestFirst')}</span><Button size="xs" variant="ghost" className="ml-auto" disabled={!feed.value?.next || feed.loading} onClick={() => setPages([...pages, feed.value!.next])}>{feed.loading && pages.length ? text('loading') : text('more')}</Button></>}>
        {feed.error !== undefined && !occurrences.length ? <EmptyState className="py-10" icon={<CircleAlert />} title={text('errorsUnavailable')} description={text('errorsUnavailableHelp')} action={<Button size="sm" variant="outline" onClick={refresh}>{text('retry')}</Button>} />
          : feed.loading && !occurrences.length ? <div className="grid gap-1 p-3">{[0, 1, 2, 3, 4, 5].map((at) => <Skeleton key={at} className="h-8" />)}</div>
          : !occurrences.length ? <EmptyState className="py-10" icon={<Bug />} title={text('noErrors')} description={chips.length || state.text ? text('noEntriesFiltered') : text('errorsEmptyHelp')} />
          : <table aria-label={text('errorsTitle')} className="w-full text-sm">
            <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-xs text-muted-foreground">
              {[text('time'), '', text('service'), text('errorField_type'), text('errorField_message'), text('errorField_environment'), text('errorField_release'), ''].map((heading, at) => <th key={at} className="h-8 px-2 font-medium whitespace-nowrap">{heading}</th>)}</tr></thead>
            <tbody>{occurrences.map((item) => <tr key={item.ref} tabIndex={0} aria-selected={item.ref === selected} onClick={() => setSelected(item.ref)}
              onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); setSelected(item.ref); } }}
              className={`cursor-pointer border-b border-border transition-colors duration-1000 last:border-b-0 hover:bg-raised focus-visible:bg-raised focus-visible:outline-none aria-selected:bg-primary/10 ${fresh.has(item.ref) ? 'bg-info/15' : ''}`}>
              <td className="h-9 px-2 text-xs whitespace-nowrap text-muted-foreground"><Timestamp value={item.time ? timestampDate(item.time) : undefined} /></td>
              <td className="px-1"><Origin origin={item.origin} /></td>
              <td className="max-w-40 px-2 font-mono text-xs"><Cell onAdd={(exclude) => add('service', item.service, exclude)}><span className="inline-flex items-center gap-1.5"><span className="size-1.5 shrink-0 rounded-full" style={{ background: serviceColor(item.service) }} />{item.service || '—'}</span></Cell></td>
              <td className="max-w-56 px-2"><Cell onAdd={(exclude) => add('type', item.type, exclude)}><span className="rounded-sm bg-destructive/10 px-1.5 py-0.5 font-mono text-xs text-destructive">{item.type || '—'}</span></Cell></td>
              <td className="max-w-[28rem] truncate px-2 text-xs" title={item.message}>{item.message || <span className="text-subtle">—</span>}</td>
              <td className="px-2 font-mono text-xs text-muted-foreground">{item.environment || '—'}</td>
              <td className="max-w-32 truncate px-2 font-mono text-xs text-muted-foreground">{item.release || '—'}</td>
              <td className="px-2 whitespace-nowrap">{item.traceId && <Waypoints className="size-3.5 text-muted-foreground" aria-label={text('traceId')} />}</td>
            </tr>)}</tbody>
          </table>}
      </Panel>
    </div>
    <DetailDrawer open={!!occurrence} onOpenChange={(open) => { if (!open) setSelected(undefined); }} size="xl"
      title={occurrence && <span className="font-mono">{occurrence.type || text('errorsTitle')}</span>}
      description={occurrence && <span className="line-clamp-2">{occurrence.message}</span>}>
      {occurrence && <ErrorDetail occurrence={occurrence} mode={mode} onAdd={(field, value, exclude) => { add(field, value, exclude); setSelected(undefined); }} />}
    </DetailDrawer>
  </section>;
}

function Origin({ origin }: { origin: ErrorOrigin }) {
  const text = usePlatformText(), sdk = origin === ErrorOrigin.SDK;
  return <span title={text(sdk ? 'originSdk' : 'originOtel')}>{sdk ? <AppWindow className="size-3.5 text-chart-1" aria-label={text('originSdk')} /> : <Server className="size-3.5 text-chart-2" aria-label={text('originOtel')} />}</span>;
}

function Cell({ onAdd, children }: { onAdd: (exclude: boolean) => void; children: React.ReactNode }) {
  const text = usePlatformText();
  return <button type="button" className="max-w-full truncate text-left hover:text-link" title={text('facetHelp')} onClick={(event) => { event.stopPropagation(); onAdd(event.altKey); }}>{children}</button>;
}

type Tab = 'stack' | 'state' | 'history' | 'trace' | 'logs' | 'fields';

function ErrorDetail({ occurrence, mode, onAdd }: { occurrence: ErrorOccurrence; mode: 'dark' | 'light'; onAdd: (field: string, value: JsonValue, exclude: boolean) => void }) {
  const text = usePlatformText(), client = useClient(ErrorServiceClient), [tab, setTab] = useState<Tab>('stack');
  const detail = usePlatformQuery(`error:${occurrence.ref}`, (signal) => client.getError(create(GetErrorRequestSchema, { ref: occurrence.ref }), { signal }));
  if (detail.error !== undefined) return <EmptyState icon={<CircleAlert />} title={text('errorGone')} description={text('errorGoneHelp')} />;
  if (!detail.value) return <Skeleton className="h-64" />;
  const sdk = occurrence.origin === ErrorOrigin.SDK;
  const tabs: Tab[] = ['stack', ...(sdk ? ['state', 'history'] as Tab[] : []), ...(occurrence.traceId ? ['trace'] as Tab[] : []), 'logs', 'fields'];
  return <div className="grid gap-3">
    <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
      <Origin origin={occurrence.origin} /><Timestamp absolute value={occurrence.time ? timestampDate(occurrence.time) : undefined} className="text-foreground" />
      <span className="font-mono">{occurrence.service}</span>{occurrence.environment && <Badge variant="outline">{occurrence.environment}</Badge>}
      {occurrence.release && <Badge variant="outline" className="font-mono">{occurrence.release}</Badge>}{occurrence.mechanism && <Badge variant="outline">{occurrence.mechanism}</Badge>}
      {detail.value.warnings.map((warning) => <Badge key={warning} variant="outline" className="border-warning/40 text-warning">{warning}</Badge>)}
    </div>
    <div role="tablist" aria-label={text('errorsTitle')} className="flex gap-1 border-b border-border">{tabs.map((entry) =>
      <button key={entry} type="button" role="tab" aria-selected={tab === entry} onClick={() => setTab(entry)}
        className="relative -mb-px h-8 border-b-2 border-transparent px-2.5 text-sm text-muted-foreground hover:text-foreground aria-selected:border-primary aria-selected:text-foreground">{text(`errorTab_${entry}`)}</button>)}</div>
    {tab === 'stack' && <StackTab detail={detail.value} mode={mode} />}
    {tab === 'state' && <StateTab detail={detail.value} mode={mode} />}
    {tab === 'history' && <HistoryTab detail={detail.value} mode={mode} />}
    {tab === 'trace' && <TraceLookup traceId={occurrence.traceId} mode={mode} />}
    {tab === 'logs' && <LogsTab occurrence={occurrence} mode={mode} />}
    {tab === 'fields' && <table className="w-full"><tbody>{Object.entries(detail.value.fields).sort(([a], [b]) => a.localeCompare(b)).map(([name, value]) => {
      const field = fieldOfStored[name];
      return field ? <FilterRow key={name} filterKey={field} name={<span className="font-mono">{name}</span>} value={value} onAdd={onAdd} />
        : <tr key={name} className="border-b border-border last:border-b-0"><td className="py-1 pr-3 align-top font-mono text-xs whitespace-nowrap text-muted-foreground">{name}</td><td className="py-1 font-mono text-xs break-all" colSpan={2}>{value}</td></tr>;
    })}</tbody></table>}
  </div>;
}

/** Stored fields a chip can filter on. */
const fieldOfStored: Record<string, ErrorFieldName> = {
  'service.name': 'service', 'deployment.environment.name': 'environment', 'exception.type': 'type', 'service.version': 'release', trace_id: 'trace_id',
  'app.debug.runtime.id': 'runtime_id', 'app.debug.group.key': 'group_key',
};

function StackTab({ detail, mode }: { detail: GetErrorResponse; mode: 'dark' | 'light' }) {
  const text = usePlatformText(), causes = useMemo(() => causesOf(detail), [detail]);
  return <div className="grid gap-3">
    <ErrorDetails error={causes} mode={mode} />
    {detail.stacktrace && <details open={!causes.frames?.length}><summary className="cursor-pointer text-xs text-muted-foreground">{text('rawStack')}</summary>
      <pre className="m-0 mt-2 max-h-80 overflow-auto rounded-md border border-border bg-raised p-3 font-mono text-xs whitespace-pre">{detail.stacktrace}</pre></details>}
  </div>;
}

function StateTab({ detail, mode }: { detail: GetErrorResponse; mode: 'dark' | 'light' }) {
  const text = usePlatformText(), state = useMemo(() => stateOf(detail.envelope as JsonValue | undefined), [detail]);
  if (!state.length) return <EmptyState className="py-8" title={text('noState')} description={text('noStateHelp')} />;
  return <div className="grid gap-3">{state.map((source) => <div key={source.name}>
    <div className="mb-1.5 font-mono text-xs text-muted-foreground">{source.name}</div>
    <JSONViewer label={source.name} value={JSON.stringify(source.value, null, 2)} mode={mode} height={200} />
  </div>)}</div>;
}

function HistoryTab({ detail, mode }: { detail: GetErrorResponse; mode: 'dark' | 'light' }) {
  const text = usePlatformText(), items = useMemo(() => historyOf(detail.envelope as JsonValue | undefined), [detail]), [open, setOpen] = useState<number>();
  if (!items.length) return <EmptyState className="py-8" title={text('noHistory')} description={text('noHistoryHelp')} />;
  return <ol className="m-0 grid list-none gap-0 p-0">{items.map((item) => <li key={item.sequence} className="border-b border-border last:border-b-0">
    <button type="button" aria-expanded={open === item.sequence} onClick={() => setOpen(open === item.sequence ? undefined : item.sequence)}
      className="flex w-full items-center gap-2 py-1.5 text-left text-xs hover:bg-raised">
      <span className="w-6 text-right text-muted-foreground tabular-nums">{item.sequence}</span>
      <Badge variant="outline" className={item.kind === 'state' ? 'text-info' : ''}>{item.kind}</Badge>
      <span className="font-mono">{item.name}</span>
      {item.time && <Timestamp className="ml-auto text-muted-foreground" value={item.time} />}
    </button>
    {open === item.sequence && item.data !== undefined && <div className="pb-2 pl-8"><JSONViewer label={item.name} value={JSON.stringify(item.data, null, 2)} mode={mode} height={160} /></div>}
  </li>)}</ol>;
}

const relations = [ErrorRelation.SAME_TRACE, ErrorRelation.SAME_SPAN, ErrorRelation.SAME_RUNTIME, ErrorRelation.TIME_WINDOW] as const;
const relationName = {
  [ErrorRelation.SAME_TRACE]: 'relation_trace', [ErrorRelation.SAME_SPAN]: 'relation_span', [ErrorRelation.SAME_RUNTIME]: 'relation_runtime', [ErrorRelation.TIME_WINDOW]: 'relation_window',
} as const;

function LogsTab({ occurrence, mode }: { occurrence: ErrorOccurrence; mode: 'dark' | 'light' }) {
  const text = usePlatformText(), client = useClient(ErrorServiceClient);
  const [relation, setRelation] = useState<(typeof relations)[number]>(occurrence.traceId ? ErrorRelation.SAME_TRACE : ErrorRelation.TIME_WINDOW);
  const logs = usePlatformQuery(`error-logs:${occurrence.ref}:${relation}`, (signal) => client.relatedLogs(create(RelatedLogsRequestSchema, { ref: occurrence.ref, relation, pageSize: 200 }), { signal }));
  const records = useMemo(() => relatedRecords(logs.value?.logs ?? []), [logs.value]);
  return <div className="grid gap-2">
    <SelectControl aria-label={text('relatedLogs')} value={String(relation)} onValueChange={(value) => setRelation(Number(value) as (typeof relations)[number])} className="max-w-72"
      options={relations.map((entry) => ({ value: String(entry), label: text(relationName[entry]) }))} />
    {logs.value && logs.value.status !== 'available' && logs.value.status !== 'partial'
      ? <EmptyState className="py-8" title={text('noRelatedLogs')} description={logs.value.reason || logs.value.status} />
      : <LogViewer label={text('relatedLogs')} records={records} mode={mode} loading={logs.loading} partial={logs.value?.status === 'partial'} />}
  </div>;
}
