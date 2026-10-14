import { useCallback, useMemo, useState, type ReactNode } from 'react';
import { create, type JsonValue } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { ErrorFacetsRequestSchema, ErrorGroupSort, ErrorGroupState, ErrorHistogramRequestSchema, ErrorInfoRequestSchema, ErrorOrigin, ErrorRelation, ErrorServiceClient,
  GetErrorGroupRequestSchema, GetErrorRequestSchema, RelatedLogsRequestSchema, SearchErrorGroupsRequestSchema, SearchErrorsRequestSchema, SetErrorGroupStateRequestSchema, type ErrorFilter,
  type ErrorGroup, type ErrorInfoResponse, type ErrorOccurrence, type GetErrorResponse } from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { BarChart } from '@backplane-labs/backplane-charts';
import { Badge, Button, ClipboardButton, ConfirmAction, DetailDrawer, EmptyState, Input, SelectControl, Panel, Skeleton, Timestamp, resolveTimeRange, type TimeRangeValue } from '@backplane-labs/backplane-ui';
import { JSONViewer } from '@backplane-labs/backplane-editors';
import { ErrorDetails, LogViewer } from '@backplane-labs/backplane-observability-ui';
import { AppWindow, BellOff, Bug, CircleAlert, CircleCheck, Layers, RotateCcw, Server, Sparkles, TriangleAlert, Waypoints } from 'lucide-react';
import { usePlatformText } from './locales.js';
import { usePlatformQuery } from './runtime.js';
import { serviceColor } from './wiring/graph.js';
import { TraceLookup } from './explore-traces.js';
import { FilterRow } from './audit.js';
import { addValue, FeedFacets, FeedFilterBar, FeedHistogram, FeedTimeBar, textOps, useFresh, valueLabel, type FeedChip, type FeedOp, type FeedTarget } from './feed.js';
import { causesOf, errorFieldEnum, errorFieldNames, errorFieldOf, errorFilterOf, exactErrorFields, groupFilterOf, historyOf, relatedRecords, reportOf, retentionText,
  stateOf, type ErrorFieldName, type ErrorGroupRef } from './errors-model.js';

/** How the errors page lists: by group (one row per distinct error) or every occurrence. */
export type ErrorsView = 'groups' | 'occurrences';
/** The order of groups. */
export type ErrorsSort = 'last_seen' | 'count' | 'first_seen';
/** Which triage states the groups list shows. */
export type ErrorsStates = 'open' | 'resolved' | 'muted' | 'all';
/** What the errors page shows; the host keeps it in the URL. */
export interface ErrorsState {
  range: TimeRangeValue; chips: FeedChip[]; text: string; /** Refresh interval, ms; 0: off. */ live: number;
  view: ErrorsView; sort: ErrorsSort; states: ErrorsStates; /** The open group's drawer. */ group?: ErrorGroupRef; /** The open occurrence's drawer. */ ref?: string;
}
export const defaultErrorsState: ErrorsState = { range: { from: 'now-24h', to: 'now', timeZone: 'browser' }, chips: [], text: '', live: 10_000, view: 'groups', sort: 'last_seen', states: 'open' };
export const errorsSorts: readonly ErrorsSort[] = ['last_seen', 'count', 'first_seen'];
export const errorsStates: readonly ErrorsStates[] = ['open', 'resolved', 'muted', 'all'];
const statesEnum: Record<ErrorsStates, ErrorGroupState[]> = {
  open: [ErrorGroupState.OPEN], resolved: [ErrorGroupState.RESOLVED], muted: [ErrorGroupState.MUTED], all: [ErrorGroupState.OPEN, ErrorGroupState.RESOLVED, ErrorGroupState.MUTED],
};
const stateName: Partial<Record<ErrorGroupState, 'open' | 'resolved' | 'muted'>> = { [ErrorGroupState.OPEN]: 'open', [ErrorGroupState.RESOLVED]: 'resolved', [ErrorGroupState.MUTED]: 'muted' };

const facetFields: readonly ErrorFieldName[] = ['service', 'type', 'environment', 'release', 'handled'];
const exactOps: readonly FeedOp[] = ['is', 'is_not'];
const sortEnum: Record<ErrorsSort, ErrorGroupSort> = { last_seen: ErrorGroupSort.LAST_SEEN, count: ErrorGroupSort.COUNT, first_seen: ErrorGroupSort.FIRST_SEEN };
type Mode = 'dark' | 'light';

/**
 * Errors from the platform's error store: exceptions the browser SDK
 * captures and any OpenTelemetry exception log (the Go SDK's CaptureError
 * and recovered panics), grouped by fingerprint or listed one by one,
 * narrowed by field conditions, text and a time range, with a histogram,
 * value counts, a live refresh, a group drawer (trend, its occurrences) and
 * an occurrence drawer (stack, the SDK's state and history, trace, related
 * logs, environment, fields). `service` pins a service condition.
 */
export function ErrorsExplorer({ mode, service, state, onStateChange }: { mode: Mode; service?: string; state: ErrorsState; onStateChange: (state: ErrorsState) => void }) {
  const text = usePlatformText(), client = useClient(ErrorServiceClient);
  const [tick, setTick] = useState(0), [pages, setPages] = useState<string[]>([]);
  const chips = useMemo(() => service ? [{ key: 'service', op: 'is' as const, values: [service] }, ...state.chips] : state.chips, [service, state.chips]);
  const range = useMemo(() => resolveTimeRange(state.range), [state.range, tick]); // eslint-disable-line react-hooks/exhaustive-deps
  const filter = useMemo(() => range && errorFilterOf(chips, state.text, range), [chips, state.text, range]);
  const key = JSON.stringify([chips, state.text, state.range]);
  const setChips = (next: FeedChip[]) => { setPages([]); onStateChange({ ...state, chips: next }); };
  const add = (field: string, value: JsonValue, exclude = false) => setChips(addValue(state.chips, field, value, exclude));
  const setView = (view: ErrorsView) => { setPages([]); onStateChange({ ...state, view }); };

  const info = usePlatformQuery('errors-info', (signal) => client.errorInfo(create(ErrorInfoRequestSchema, {}), { signal }));
  const groups = usePlatformQuery(`errors-groups:${key}:${state.sort}:${state.states}:${tick}:${state.view}:${pages.join(',')}`, async (signal) => {
    if (state.view !== 'groups') return undefined;
    const groups: ErrorGroup[] = [];
    let next = '', step = 0n;
    for (const cursor of ['', ...pages]) {
      const page = await client.searchErrorGroups(create(SearchErrorGroupsRequestSchema, { filter, pageSize: 50, pageCursor: cursor, sort: sortEnum[state.sort], states: statesEnum[state.states] }), { signal });
      groups.push(...page.groups); next = page.nextPageCursor; step = page.stepMillis;
    }
    return { groups, next, step };
  }, { keep: true });
  const feed = usePlatformQuery(`errors-feed:${key}:${tick}:${state.view}:${pages.join(',')}`, async (signal) => {
    if (state.view !== 'occurrences') return undefined;
    const occurrences: ErrorOccurrence[] = [];
    let next = '';
    for (const cursor of ['', ...pages]) {
      const page = await client.searchErrors(create(SearchErrorsRequestSchema, { filter, pageSize: 100, pageCursor: cursor }), { signal });
      occurrences.push(...page.occurrences); next = page.nextPageCursor;
    }
    return { occurrences, next };
  }, { keep: true });
  const histogram = usePlatformQuery(`errors-histogram:${key}:${tick}`, (signal) => client.errorHistogram(create(ErrorHistogramRequestSchema, { filter }), { signal }), { keep: true });
  const facets = usePlatformQuery(`errors-facets:${key}:${tick}`, (signal) =>
    client.errorFacets(create(ErrorFacetsRequestSchema, { filter, fields: facetFields.map((field) => errorFieldEnum[field]), limit: 8 }), { signal }), { keep: true });
  const facetValues = useMemo(() => (facets.value?.facets ?? []).map((facet) => {
    const field = errorFieldOf(facet.field);
    return { key: field, label: text(`errorField_${field}`), values: facet.values.map((entry) => ({ value: entry.value as JsonValue, count: Number(entry.count) })) };
  }), [facets.value, text]);
  const targets = useMemo<FeedTarget[]>(() => errorFieldNames.map((field) => ({ key: field, label: text(`errorField_${field}`), ops: exactErrorFields.includes(field) ? exactOps : textOps })), [text]);
  const suggest = (field: string) => field === 'origin' ? ['sdk', 'otel_log'] : field === 'handled' ? ['true', 'false'] : field === 'ingest' ? ['trusted', 'proxy']
    : facetValues.find((facet) => facet.key === field)?.values.map((entry) => entry.value) ?? [];

  const listed = state.view === 'groups' ? groups : feed;
  const rows = state.view === 'groups' ? groups.value?.groups.length ?? 0 : feed.value?.occurrences.length ?? 0;
  const next = state.view === 'groups' ? groups.value?.next : feed.value?.next;
  const fresh = useFresh(useMemo(() => state.view === 'groups' ? groups.value?.groups.map((group) => `${group.fingerprint}@${group.service}`) : feed.value?.occurrences.map((occurrence) => occurrence.ref),
    [state.view, groups.value, feed.value]), `${state.view}:${key}`);
  const refresh = useCallback(() => setTick((value) => value + 1), []);
  const time = (bucket: { start?: Parameters<typeof timestampDate>[0] }) => bucket.start ? timestampDate(bucket.start).getTime() : 0;
  const retention = retentionText(info.value?.retention);
  const empty = chips.length || state.text ? text('noEntriesFiltered') : `${text('errorsEmptyHelp')} ${retention ? text('errorsRetention', { retention }) : text('errorsRetentionForever')}`;
  const viewButton = (view: ErrorsView, icon: ReactNode) => <Button size="xs" variant={state.view === view ? 'secondary' : 'ghost'} aria-pressed={state.view === view} onClick={() => setView(view)}>{icon}{text(`errorsView_${view}`)}</Button>;

  return <section aria-label={text('errorsTitle')} className="flex h-full min-h-0 flex-col gap-3">
    <FeedFilterBar chips={state.chips} targets={targets} onChips={setChips} text={state.text} onText={(value) => onStateChange({ ...state, text: value })} suggest={suggest}
      pinned={service && <span className="inline-flex h-7 items-center rounded-md border border-border bg-raised px-2 font-mono text-xs">{text('serviceScope', { service })}</span>} />
    <FeedTimeBar range={state.range} onRange={(value) => onStateChange({ ...state, range: value })} live={state.live} onLive={(live) => onStateChange({ ...state, live })} onRefresh={refresh} mode={mode} />
    {!range && <p role="alert" className="m-0 text-xs text-destructive">{text('invalidTimeRange')}</p>}
    <FeedHistogram title={text('errorsOverTime')} mode={mode} loading={histogram.loading} count={histogram.value ? Number(histogram.value.total) : undefined}
      series={histogram.value && [{ id: 'errors', label: text('errorsTitle'), color: 'var(--destructive)', points: histogram.value.buckets.map((bucket) => ({ x: time(bucket), value: bucket.count })) }]}
      onRange={(from, to) => onStateChange({ ...state, range: { ...state.range, from: new Date(from).toISOString(), to: new Date(to).toISOString() } })} />
    <div className="grid min-h-0 flex-1 grid-cols-[240px_minmax(0,1fr)] gap-3">
      <FeedFacets facets={facetValues} chips={chips} onAdd={add} decorate={(field, value) => field === 'service'
        ? { prefix: <span className="relative size-1.5 shrink-0 rounded-full" style={{ background: serviceColor(valueLabel(value)) }} /> } : undefined} />
      <Panel fill flush title={<div role="group" aria-label={text('errorsView')} className="flex items-center gap-1">{viewButton('groups', <Layers />)}{viewButton('occurrences', <Bug />)}</div>} count={rows}
        actions={state.view === 'groups' && <div className="flex items-center gap-1">
          <SelectControl aria-label={text('errorsStates')} value={state.states} onValueChange={(value) => { setPages([]); onStateChange({ ...state, states: value as ErrorsStates }); }} className="h-7 w-32"
            options={errorsStates.map((states) => ({ value: states, label: text(`errorsStates_${states}`) }))} />
          <SelectControl aria-label={text('sort')} value={state.sort} onValueChange={(value) => { setPages([]); onStateChange({ ...state, sort: value as ErrorsSort }); }} className="h-7 w-40"
            options={errorsSorts.map((sort) => ({ value: sort, label: text(`groupSort_${sort}`) }))} /></div>}
        footer={<><span>{state.view === 'groups' ? text(`groupSort_${state.sort}`) : text('newestFirst')}</span>
          <Button size="xs" variant="ghost" className="ml-auto" disabled={!next || listed.loading} onClick={() => setPages([...pages, next!])}>{listed.loading && pages.length ? text('loading') : text('more')}</Button></>}>
        {listed.error !== undefined && !rows ? <EmptyState className="py-10" icon={<CircleAlert />} title={text('errorsUnavailable')} description={text('errorsUnavailableHelp')} action={<Button size="sm" variant="outline" onClick={refresh}>{text('retry')}</Button>} />
          : listed.loading && !rows ? <div className="grid gap-1 p-3">{[0, 1, 2, 3, 4, 5].map((at) => <Skeleton key={at} className="h-8" />)}</div>
          : !rows ? <EmptyState className="py-10" icon={<Bug />} title={text(state.view === 'groups' ? 'noGroups' : 'noErrors')} description={empty} />
          : state.view === 'groups'
            ? <GroupsTable groups={groups.value?.groups ?? []} fresh={fresh} selected={state.group} onSelect={(group) => onStateChange({ ...state, group })} onAdd={add} />
            : <OccurrencesTable occurrences={feed.value?.occurrences ?? []} fresh={fresh} selected={state.ref} onSelect={(ref) => onStateChange({ ...state, ref })} onAdd={add} />}
      </Panel>
    </div>
    <DetailDrawer open={!!state.group} onOpenChange={(open) => { if (!open) onStateChange({ ...state, group: undefined }); }} size="lg"
      title={<span className="inline-flex items-center gap-2"><Layers className="size-4 text-muted-foreground" />{text('errorGroup')}</span>}
      description={state.group && <span className="font-mono">{state.group.service} · {state.group.fingerprint}</span>}>
      {state.group && filter && <GroupDetail group={state.group} filter={filter} filterKey={`${key}:${tick}`} mode={mode} onOpen={(ref) => onStateChange({ ...state, ref })} onChanged={refresh}
        onAdd={(field, value, exclude) => { add(field, value, exclude); onStateChange({ ...state, chips: addValue(state.chips, field, value, exclude), group: undefined }); }} />}
    </DetailDrawer>
    <DetailDrawer open={!!state.ref} onOpenChange={(open) => { if (!open) onStateChange({ ...state, ref: undefined }); }} size="xl"
      title={<span className="inline-flex items-center gap-2"><Bug className="size-4 text-muted-foreground" />{text('occurrence')}</span>} description={state.ref && <span className="font-mono">{state.ref}</span>}>
      {state.ref && <ErrorDetail refId={state.ref} mode={mode} info={info.value} onAdd={(field, value, exclude) => onStateChange({ ...state, chips: addValue(state.chips, field, value, exclude), ref: undefined, group: undefined })}
        onOpenGroup={(group) => onStateChange({ ...state, group, ref: undefined })} />}
    </DetailDrawer>
  </section>;
}

function Origin({ origin }: { origin: ErrorOrigin }) {
  const text = usePlatformText(), sdk = origin === ErrorOrigin.SDK;
  return <span title={text(sdk ? 'originSdk' : 'originOtel')}>{sdk ? <AppWindow className="size-3.5 text-chart-1" aria-label={text('originSdk')} /> : <Server className="size-3.5 text-chart-2" aria-label={text('originOtel')} />}</span>;
}

function Cell({ onAdd, children }: { onAdd: (exclude: boolean) => void; children: ReactNode }) {
  const text = usePlatformText();
  return <button type="button" className="max-w-full truncate text-left hover:text-link" title={text('facetHelp')} onClick={(event) => { event.stopPropagation(); onAdd(event.altKey); }}>{children}</button>;
}

const rowClass = (selected: boolean, fresh: boolean) =>
  `cursor-pointer border-b border-border transition-colors duration-1000 last:border-b-0 hover:bg-raised focus-visible:bg-raised focus-visible:outline-none ${selected ? 'bg-primary/10' : ''} ${fresh ? 'bg-info/15' : ''}`;
const onEnter = (fn: () => void) => (event: React.KeyboardEvent) => { if (event.key === 'Enter') { event.preventDefault(); fn(); } };

/** Occurrences per bucket as a tiny bar chart. */
export function Sparkline({ values, label }: { values: readonly bigint[]; label: string }) {
  const max = values.reduce((top, value) => value > top ? value : top, 0n);
  const width = 72, height = 18, bar = width / Math.max(values.length, 1);
  return <svg role="img" aria-label={label} width={width} height={height} className="block overflow-visible text-destructive">
    {values.map((value, at) => { const h = max > 0n ? Math.max(value > 0n ? 1.5 : 0, (Number(value) / Number(max)) * height) : 0;
      return <rect key={at} x={at * bar} y={height - h} width={Math.max(bar - 0.5, 0.5)} height={h} fill="currentColor" opacity={value > 0n ? 0.85 : 0} />; })}
  </svg>;
}

function Flags({ unhandled, fresh, truncated, state, regressed }: { unhandled?: boolean; fresh?: boolean; truncated?: boolean; state?: ErrorGroupState; regressed?: boolean }) {
  const text = usePlatformText();
  return <>
    {state === ErrorGroupState.RESOLVED && <Badge variant="outline" className="border-success/40 text-success"><CircleCheck />{text('groupState_resolved')}</Badge>}
    {state === ErrorGroupState.MUTED && <Badge variant="outline" className="text-muted-foreground"><BellOff />{text('groupState_muted')}</Badge>}
    {regressed && <Badge variant="outline" className="border-warning/40 text-warning"><RotateCcw />{text('regressed')}</Badge>}
    {fresh && <Badge variant="outline" className="border-info/40 text-info"><Sparkles />{text('newGroup')}</Badge>}
    {unhandled && <Badge variant="destructive"><TriangleAlert />{text('unhandled')}</Badge>}
    {truncated && <Badge variant="outline" className="border-warning/40 text-warning">{text('truncated')}</Badge>}
  </>;
}

function GroupsTable({ groups, fresh, selected, onSelect, onAdd }: {
  groups: readonly ErrorGroup[]; fresh: ReadonlySet<string>; selected?: ErrorGroupRef; onSelect: (group: ErrorGroupRef) => void; onAdd: (field: string, value: JsonValue, exclude: boolean) => void;
}) {
  const text = usePlatformText();
  const headings: [string, string][] = [['', 'w-7'], [text('errorsTitle'), ''], [text('service'), 'w-36'], [text('trend'), 'w-24'], [text('inRange'), 'w-20 text-right'], [text('totalCount'), 'w-16 text-right'],
    [text('lastSeen'), 'w-28'], [text('firstSeen'), 'hidden w-28 xl:table-cell']];
  return <table aria-label={text('errorsTitle')} className="w-full table-fixed text-sm">
    <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-xs text-muted-foreground">
      {headings.map(([heading, className], at) => <th key={at} className={`h-8 px-2 font-medium whitespace-nowrap ${className}`}>{heading}</th>)}</tr></thead>
    <tbody>{groups.map((group) => { const id = `${group.fingerprint}@${group.service}`, open = () => onSelect({ fingerprint: group.fingerprint, service: group.service });
      return <tr key={id} tabIndex={0} aria-selected={selected?.fingerprint === group.fingerprint && selected.service === group.service} onClick={open} onKeyDown={onEnter(open)}
        className={rowClass(selected?.fingerprint === group.fingerprint && selected.service === group.service, fresh.has(id))}>
        <td className="px-1"><Origin origin={group.origin} /></td>
        <td className="min-w-0 px-2 py-1.5"><div className="flex flex-wrap items-center gap-1.5"><Cell onAdd={(exclude) => onAdd('type', group.type, exclude)}><span className="rounded-sm bg-destructive/10 px-1.5 py-0.5 font-mono text-xs text-destructive">{group.type || '—'}</span></Cell>
          <Flags unhandled={group.unhandled} fresh={group.new} state={group.state} regressed={!!group.regressedAt} /></div>
          <div className="truncate text-xs text-muted-foreground" title={group.message}>{group.message || '—'}</div>
          {(group.environments.length > 0 || group.releases.length > 0) && <div className="mt-0.5 flex flex-wrap gap-1 font-mono text-2xs text-subtle">{[...group.environments, ...group.releases].map((value) => <span key={value}>{value}</span>)}</div>}</td>
        <td className="truncate px-2 font-mono text-xs"><Cell onAdd={(exclude) => onAdd('service', group.service, exclude)}><span className="inline-flex items-center gap-1.5"><span className="size-1.5 shrink-0 rounded-full" style={{ background: serviceColor(group.service) }} />{group.service}</span></Cell></td>
        <td className="px-2"><Sparkline values={group.trend} label={text('trend')} /></td>
        <td className="px-2 text-right text-xs tabular-nums">{String(group.countInRange)}</td>
        <td className="px-2 text-right text-xs text-muted-foreground tabular-nums">{String(group.count)}</td>
        <td className="px-2 text-xs whitespace-nowrap text-muted-foreground"><Timestamp value={group.lastSeen ? timestampDate(group.lastSeen) : undefined} /></td>
        <td className="hidden px-2 text-xs whitespace-nowrap text-muted-foreground xl:table-cell"><Timestamp value={group.firstSeen ? timestampDate(group.firstSeen) : undefined} /></td>
      </tr>; })}</tbody>
  </table>;
}

function OccurrencesTable({ occurrences, fresh, selected, onSelect, onAdd, compact }: {
  occurrences: readonly ErrorOccurrence[]; fresh?: ReadonlySet<string>; selected?: string; onSelect: (ref: string) => void; onAdd: (field: string, value: JsonValue, exclude: boolean) => void; compact?: boolean;
}) {
  const text = usePlatformText();
  const headings = compact ? [text('time'), '', text('errorField_message'), text('errorField_environment'), text('errorField_release'), '']
    : [text('time'), '', text('service'), text('errorField_type'), text('errorField_message'), text('errorField_environment'), text('errorField_release'), ''];
  return <table aria-label={text(compact ? 'occurrencesOf' : 'errorsTitle')} className="w-full text-sm">
    <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-xs text-muted-foreground">
      {headings.map((heading, at) => <th key={at} className="h-8 px-2 font-medium whitespace-nowrap">{heading}</th>)}</tr></thead>
    <tbody>{occurrences.map((item) => <tr key={item.ref} tabIndex={0} aria-selected={item.ref === selected} onClick={() => onSelect(item.ref)} onKeyDown={onEnter(() => onSelect(item.ref))}
      className={rowClass(item.ref === selected, fresh?.has(item.ref) ?? false)}>
      <td className="h-9 px-2 text-xs whitespace-nowrap text-muted-foreground"><Timestamp value={item.time ? timestampDate(item.time) : undefined} /></td>
      <td className="px-1"><span className="inline-flex items-center gap-1"><Origin origin={item.origin} /><Flags unhandled={item.unhandled} truncated={item.truncated} /></span></td>
      {!compact && <td className="max-w-40 px-2 font-mono text-xs"><Cell onAdd={(exclude) => onAdd('service', item.service, exclude)}><span className="inline-flex items-center gap-1.5"><span className="size-1.5 shrink-0 rounded-full" style={{ background: serviceColor(item.service) }} />{item.service || '—'}</span></Cell></td>}
      {!compact && <td className="max-w-56 px-2"><Cell onAdd={(exclude) => onAdd('type', item.type, exclude)}><span className="rounded-sm bg-destructive/10 px-1.5 py-0.5 font-mono text-xs text-destructive">{item.type || '—'}</span></Cell></td>}
      <td className="max-w-[28rem] truncate px-2 text-xs" title={item.message}>{item.message || <span className="text-subtle">—</span>}</td>
      <td className="px-2 font-mono text-xs text-muted-foreground">{item.environment || '—'}</td>
      <td className="max-w-32 truncate px-2 font-mono text-xs text-muted-foreground">{item.release || '—'}</td>
      <td className="px-2 whitespace-nowrap">{item.traceId && <Waypoints className="size-3.5 text-muted-foreground" aria-label={text('traceId')} />}</td>
    </tr>)}</tbody>
  </table>;
}

function Fact({ name, children, mono }: { name: string; children: ReactNode; mono?: boolean }) {
  return <div className="min-w-0"><div className="text-2xs font-medium tracking-wide text-muted-foreground uppercase">{name}</div><div className={`truncate text-xs ${mono ? 'font-mono' : ''}`}>{children}</div></div>;
}

/** A group over the page's range: its counts, trend and occurrences. */
function GroupDetail({ group, filter, filterKey, mode, onOpen, onAdd, onChanged }: {
  group: ErrorGroupRef; filter: ErrorFilter; /** Names the filter (bigint timestamps do not stringify). */ filterKey: string; mode: Mode; onOpen: (ref: string) => void;
  onAdd: (field: string, value: JsonValue, exclude: boolean) => void; /** The group's state changed: the list is stale. */ onChanged: () => void;
}) {
  const text = usePlatformText(), client = useClient(ErrorServiceClient), [pages, setPages] = useState<string[]>([]), [note, setNote] = useState('');
  const key = `${group.fingerprint}@${group.service}:${filterKey}`;
  const detail = usePlatformQuery(`error-group:${key}`, (signal) => client.getErrorGroup(create(GetErrorGroupRequestSchema, { fingerprint: group.fingerprint, service: group.service, filter }), { signal }), { keep: true });
  const narrowed = useMemo(() => groupFilterOf(filter, group), [filter, group]);
  const feed = usePlatformQuery(`error-group-feed:${key}:${pages.join(',')}`, async (signal) => {
    const occurrences: ErrorOccurrence[] = [];
    let next = '';
    for (const cursor of ['', ...pages]) {
      const page = await client.searchErrors(create(SearchErrorsRequestSchema, { filter: narrowed, pageSize: 50, pageCursor: cursor }), { signal });
      occurrences.push(...page.occurrences); next = page.nextPageCursor;
    }
    return { occurrences, next };
  }, { keep: true });
  if (detail.error !== undefined) return <EmptyState icon={<CircleAlert />} title={text('errorGone')} description={text('errorGoneHelp')} />;
  if (!detail.value?.group) return <Skeleton className="h-64" />;
  const g = detail.value.group, time = (bucket: { start?: Parameters<typeof timestampDate>[0] }) => bucket.start ? timestampDate(bucket.start).getTime() : 0;
  return <div className="grid gap-3">
    <div className="flex flex-wrap items-center gap-2"><Origin origin={g.origin} />
      <Cell onAdd={(exclude) => onAdd('type', g.type, exclude)}><span className="rounded-sm bg-destructive/10 px-1.5 py-0.5 font-mono text-xs text-destructive">{g.type || '—'}</span></Cell>
      <Cell onAdd={(exclude) => onAdd('service', g.service, exclude)}><span className="font-mono text-xs">{g.service}</span></Cell>
      <Flags unhandled={g.unhandled} fresh={g.new} state={g.state} regressed={!!g.regressedAt} />
      <span className="ml-auto flex items-center gap-1">{([
        ['resolved', ErrorGroupState.RESOLVED, <CircleCheck key="r" />], ['muted', ErrorGroupState.MUTED, <BellOff key="m" />], ['open', ErrorGroupState.OPEN, <RotateCcw key="o" />],
      ] as const).filter(([, value]) => value !== g.state && (value !== ErrorGroupState.OPEN || g.state !== ErrorGroupState.UNSPECIFIED)).map(([name, value, icon]) =>
        <ConfirmAction key={name} trigger={<>{icon}{text(`setState_${name}`)}</>} title={text(`setState_${name}Confirm`)} description={text(`setState_${name}Help`)} confirmLabel={text(`setState_${name}`)}
          onConfirm={async (signal) => {
            await client.setErrorGroupState(create(SetErrorGroupStateRequestSchema, { fingerprint: g.fingerprint, service: g.service, state: value, note }), { signal });
            if (!signal.aborted) { setNote(''); detail.refresh(); onChanged(); }
          }}><Input aria-label={text('stateNote')} placeholder={text('stateNoteHint')} value={note} onChange={(event) => setNote(event.target.value)} maxLength={1024} /></ConfirmAction>)}</span></div>
    {g.message && <p className="m-0 rounded-md border border-border bg-raised px-3 py-2 font-mono text-xs break-all">{g.message}</p>}
    {g.state !== ErrorGroupState.OPEN && g.state !== ErrorGroupState.UNSPECIFIED && <p className="m-0 text-xs text-muted-foreground">
      {text('stateBy', { state: text(`groupState_${stateName[g.state] ?? 'open'}`), by: g.stateBy || '—' })} <Timestamp value={g.stateAt ? timestampDate(g.stateAt) : undefined} />{g.stateNote && <> · <span className="text-foreground">{g.stateNote}</span></>}</p>}
    {g.regressedAt && <p className="m-0 text-xs text-warning">{text('regressedAt')} <Timestamp value={timestampDate(g.regressedAt)} />{g.stateNote && <> · {g.stateNote}</>}</p>}
    <div className="grid grid-cols-3 gap-3 sm:grid-cols-5">
      <Fact name={text('inRange')}>{String(g.countInRange)}</Fact><Fact name={text('totalCount')}>{String(g.count)} <span className="text-muted-foreground">({text('storedCount', { count: Number(g.stored) })})</span></Fact>
      <Fact name={text('firstSeen')}><Timestamp value={g.firstSeen ? timestampDate(g.firstSeen) : undefined} /></Fact><Fact name={text('lastSeen')}><Timestamp value={g.lastSeen ? timestampDate(g.lastSeen) : undefined} /></Fact>
      <Fact name={text('errorField_fingerprint')} mono><span title={g.fingerprint}><Cell onAdd={(exclude) => onAdd('fingerprint', g.fingerprint, exclude)}>{g.fingerprint}</Cell></span></Fact>
    </div>
    {(g.environments.length > 0 || g.releases.length > 0) && <div className="flex flex-wrap gap-1">
      {g.environments.map((value) => <Badge key={`e:${value}`} variant="outline" className="cursor-pointer" onClick={() => onAdd('environment', value, false)}>{value}</Badge>)}
      {g.releases.map((value) => <Badge key={`r:${value}`} variant="outline" className="cursor-pointer font-mono" onClick={() => onAdd('release', value, false)}>{value}</Badge>)}</div>}
    <Panel title={text('trend')}>{detail.value.buckets.some((bucket) => bucket.count > 0n)
      ? <BarChart label={text('trend')} mode={mode} height={110} controls={false} series={[{ id: 'group', label: text('occurrences'), color: 'var(--destructive)', points: detail.value.buckets.map((bucket) => ({ x: time(bucket), value: bucket.count })) }]} />
      : <div className="grid h-[110px] place-items-center text-xs text-muted-foreground">{text('noEntries')}</div>}</Panel>
    <Panel flush title={text('occurrencesOf')} count={feed.value?.occurrences.length} maxBodyHeight={360}
      footer={<><span>{text('newestFirst')}</span><Button size="xs" variant="ghost" className="ml-auto" disabled={!feed.value?.next || feed.loading} onClick={() => setPages([...pages, feed.value!.next])}>{text('more')}</Button></>}>
      {feed.value?.occurrences.length ? <OccurrencesTable compact occurrences={feed.value.occurrences} onSelect={onOpen} onAdd={onAdd} />
        : feed.loading ? <div className="grid gap-1 p-3">{[0, 1, 2].map((at) => <Skeleton key={at} className="h-8" />)}</div> : <EmptyState className="py-6" title={text('noErrors')} description={text('groupOutOfRange')} />}
    </Panel>
  </div>;
}

type Tab = 'stack' | 'state' | 'history' | 'trace' | 'logs' | 'environment' | 'fields';

/** One occurrence in full, read by its reference. */
function ErrorDetail({ refId, mode, info, onAdd, onOpenGroup }: {
  refId: string; mode: Mode; info?: ErrorInfoResponse; onAdd: (field: string, value: JsonValue, exclude: boolean) => void; onOpenGroup: (group: ErrorGroupRef) => void;
}) {
  const text = usePlatformText(), client = useClient(ErrorServiceClient), [tab, setTab] = useState<Tab>('stack');
  const detail = usePlatformQuery(`error:${refId}`, (signal) => client.getError(create(GetErrorRequestSchema, { ref: refId }), { signal }));
  const report = useMemo(() => detail.value ? reportOf(detail.value) : '', [detail.value]);
  if (detail.error !== undefined) return <EmptyState icon={<CircleAlert />} title={text('errorGone')} description={text('errorGoneHelp')} />;
  if (!detail.value?.occurrence) return <Skeleton className="h-64" />;
  const value = detail.value, occurrence = value.occurrence!, sdk = occurrence.origin === ErrorOrigin.SDK;
  const tabs: Tab[] = ['stack', ...(sdk ? ['state', 'history'] as Tab[] : []), ...(occurrence.traceId ? ['trace'] as Tab[] : []), ...(info?.relatedAvailable !== false ? ['logs'] as Tab[] : []), 'environment', 'fields'];
  return <div className="grid min-w-0 gap-3">
    <div className="flex flex-wrap items-center gap-2">
      <Cell onAdd={(exclude) => onAdd('type', occurrence.type, exclude)}><span className="rounded-sm bg-destructive/10 px-1.5 py-0.5 font-mono text-sm text-destructive">{occurrence.type || text('errorsTitle')}</span></Cell>
      <Flags unhandled={occurrence.unhandled} truncated={occurrence.truncated} />
      {value.warnings.filter((warning) => warning !== 'truncated').map((warning) => <Badge key={warning} variant="outline" className="border-warning/40 text-warning">{warning}</Badge>)}
      <span className="ml-auto"><ClipboardButton value={report} label={text('copyReport')} /></span>
    </div>
    {occurrence.message && <p className="m-0 rounded-md border border-border bg-raised px-3 py-2 font-mono text-xs break-all">{occurrence.message}</p>}
    <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
      <Origin origin={occurrence.origin} /><Timestamp absolute value={occurrence.time ? timestampDate(occurrence.time) : undefined} className="text-foreground" />
      <Cell onAdd={(exclude) => onAdd('service', occurrence.service, exclude)}><span className="font-mono">{occurrence.service}</span></Cell>
      {occurrence.environment && <Badge variant="outline">{occurrence.environment}</Badge>}{occurrence.release && <Badge variant="outline" className="font-mono">{occurrence.release}</Badge>}
      {occurrence.mechanism && <Badge variant="outline">{occurrence.mechanism}</Badge>}
      {value.group && <Button size="xs" variant="outline" className="ml-auto" onClick={() => onOpenGroup({ fingerprint: value.group!.fingerprint, service: value.group!.service })}>
        <Layers />{text('groupSummary', { count: Number(value.group.count) })}</Button>}
      {value.group && <Flags state={value.group.state} regressed={!!value.group.regressedAt} />}
    </div>
    <div role="tablist" aria-label={text('errorsTitle')} className="flex gap-1 border-b border-border">{tabs.map((entry) =>
      <button key={entry} type="button" role="tab" aria-selected={tab === entry} onClick={() => setTab(entry)}
        className="relative -mb-px h-8 border-b-2 border-transparent px-2.5 text-sm text-muted-foreground hover:text-foreground aria-selected:border-primary aria-selected:text-foreground">{text(`errorTab_${entry}`)}</button>)}</div>
    {tab === 'stack' && <StackTab detail={value} mode={mode} />}
    {tab === 'state' && <StateTab detail={value} mode={mode} />}
    {tab === 'history' && <HistoryTab detail={value} mode={mode} />}
    {tab === 'trace' && <TraceLookup traceId={occurrence.traceId} mode={mode} />}
    {tab === 'logs' && <LogsTab occurrence={occurrence} mode={mode} />}
    {tab === 'environment' && <EnvironmentTab detail={value} mode={mode} onAdd={onAdd} />}
    {tab === 'fields' && <table className="w-full"><tbody>{Object.entries(value.fields).sort(([a], [b]) => a.localeCompare(b)).map(([name, fieldValue]) => {
      const field = fieldOfStored[name];
      return field ? <FilterRow key={name} filterKey={field} name={<span className="font-mono">{name}</span>} value={fieldValue} onAdd={onAdd} />
        : <tr key={name} className="border-b border-border last:border-b-0"><td className="py-1 pr-3 align-top font-mono text-xs whitespace-nowrap text-muted-foreground">{name}</td><td className="py-1 font-mono text-xs break-all" colSpan={2}>{fieldValue}</td></tr>;
    })}</tbody></table>}
  </div>;
}

/** Stored fields a chip can filter on. */
const fieldOfStored: Record<string, ErrorFieldName> = {
  'service.name': 'service', 'deployment.environment.name': 'environment', 'exception.type': 'type', 'service.version': 'release', 'service.instance.id': 'instance', trace_id: 'trace_id',
  'app.debug.runtime.id': 'runtime_id', 'app.debug.group.key': 'group_key', 'app.debug.exception.handled': 'handled', 'backplane.fingerprint': 'fingerprint', 'backplane.ingest': 'ingest', 'backplane.origin': 'origin',
};

/** Where the occurrence came from: its resource, the record's attributes, its scope and how the platform took it in. */
function EnvironmentTab({ detail, mode, onAdd }: { detail: GetErrorResponse; mode: Mode; onAdd: (field: string, value: JsonValue, exclude: boolean) => void }) {
  const text = usePlatformText(), o = detail.occurrence!;
  const facts: [ErrorFieldName | undefined, string, string][] = [
    ['service', text('service'), o.service], ['environment', text('errorField_environment'), o.environment], ['release', text('errorField_release'), o.release],
    ['instance', text('errorField_instance'), detail.fields['service.instance.id'] ?? ''], ['origin', text('errorField_origin'), o.origin === ErrorOrigin.SDK ? 'sdk' : 'otel_log'],
    ['ingest', text('errorField_ingest'), o.ingest], [undefined, text('receivedAt'), o.receivedAt ? timestampDate(o.receivedAt).toISOString() : ''], [undefined, text('scope'), detail.scope],
    ['runtime_id', text('errorField_runtime_id'), o.runtimeId], ['group_key', text('errorField_group_key'), o.groupKey],
  ];
  return <div className="grid min-w-0 gap-3">
    <table className="w-full table-fixed"><colgroup><col className="w-48" /><col /><col className="w-14" /></colgroup><tbody>{facts.filter(([, , value]) => value).map(([field, name, value]) => field
      ? <FilterRow key={name} filterKey={field} name={name} value={value} onAdd={onAdd} />
      : <tr key={name} className="border-b border-border last:border-b-0"><td className="py-1 pr-3 align-top text-xs whitespace-nowrap text-muted-foreground">{name}</td><td className="py-1 font-mono text-xs break-all" colSpan={2}>{value}</td></tr>)}</tbody></table>
    {detail.resource && Object.keys(detail.resource).length > 0 && <div className="min-w-0 overflow-hidden"><div className="mb-1.5 text-xs text-muted-foreground">{text('resource')}</div>
      <JSONViewer label={text('resource')} value={JSON.stringify(detail.resource, null, 2)} mode={mode} height={160} /></div>}
    {detail.attributes && Object.keys(detail.attributes).length > 0 && <div className="min-w-0 overflow-hidden"><div className="mb-1.5 text-xs text-muted-foreground">{text('attributes')}</div>
      <JSONViewer label={text('attributes')} value={JSON.stringify(detail.attributes, null, 2)} mode={mode} height={200} /></div>}
  </div>;
}

function StackTab({ detail, mode }: { detail: GetErrorResponse; mode: Mode }) {
  const text = usePlatformText(), causes = useMemo(() => causesOf(detail), [detail]);
  return <div className="grid gap-3">
    <ErrorDetails error={causes} mode={mode} />
    {detail.stacktrace && <details open={!causes.frames?.length}><summary className="cursor-pointer text-xs text-muted-foreground">{text('rawStack')}</summary>
      <pre className="m-0 mt-2 max-h-80 overflow-auto rounded-md border border-border bg-raised p-3 font-mono text-xs whitespace-pre">{detail.stacktrace}</pre></details>}
  </div>;
}

function StateTab({ detail, mode }: { detail: GetErrorResponse; mode: Mode }) {
  const text = usePlatformText(), state = useMemo(() => stateOf(detail.envelope as JsonValue | undefined), [detail]);
  if (!state.length) return <EmptyState className="py-8" title={text('noState')} description={text('noStateHelp')} />;
  return <div className="grid gap-3">{state.map((source) => <div key={source.name}>
    <div className="mb-1.5 font-mono text-xs text-muted-foreground">{source.name}</div>
    <JSONViewer label={source.name} value={JSON.stringify(source.value, null, 2)} mode={mode} height={200} />
  </div>)}</div>;
}

function HistoryTab({ detail, mode }: { detail: GetErrorResponse; mode: Mode }) {
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

function LogsTab({ occurrence, mode }: { occurrence: ErrorOccurrence; mode: Mode }) {
  const text = usePlatformText(), client = useClient(ErrorServiceClient);
  const [relation, setRelation] = useState<(typeof relations)[number]>(occurrence.traceId ? ErrorRelation.SAME_TRACE : ErrorRelation.TIME_WINDOW);
  const logs = usePlatformQuery(`error-logs:${occurrence.ref}:${relation}`, (signal) => client.relatedLogs(create(RelatedLogsRequestSchema, { ref: occurrence.ref, relation, pageSize: 200 }), { signal }));
  const records = useMemo(() => relatedRecords(logs.value?.logs ?? []), [logs.value]);
  return <div className="grid min-w-0 gap-2 overflow-hidden">
    <SelectControl aria-label={text('relatedLogs')} value={String(relation)} onValueChange={(value) => setRelation(Number(value) as (typeof relations)[number])} className="max-w-72"
      options={relations.map((entry) => ({ value: String(entry), label: text(relationName[entry]) }))} />
    {logs.value && logs.value.status !== 'available' && logs.value.status !== 'partial'
      ? <EmptyState className="py-8" title={text('noRelatedLogs')} description={logs.value.reason || logs.value.status} />
      : <LogViewer label={text('relatedLogs')} records={records} mode={mode} loading={logs.loading} partial={logs.value?.status === 'partial'} />}
  </div>;
}
