import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { create } from '@bufbuild/protobuf';
import { ObsServiceClient, GetObsCapabilitiesRequestSchema, GetObsSelectorsRequestSchema, ListObsFieldsRequestSchema, ListObsFieldValuesRequestSchema, ListObsSourcesRequestSchema, QueryObsRequestSchema, type QueryObsResponse } from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { Badge, Button, DetailDrawer, EmptyState, FilterCombo, Input, SelectControl, Panel, Skeleton, Switch } from '@backplane-labs/backplane-ui';
import { CodeEditor, type QueryLanguage } from '@backplane-labs/backplane-editors';
import type { CorrelationTarget } from '@backplane-labs/backplane-observability-ui';
import { Activity, ChevronRight, CircleAlert, FileText, LayoutDashboard, Play, Radio, Search, Waypoints } from 'lucide-react';
import { usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { buildQuery, defaultFunction, languageIds, languageLabel, languageNames, nanosNow, ranges, signalIds, stepFor, type ExploreRange, type ExploreSignal, type MetricFunction, type QueryBuilder } from './explore-query.js';
import { LogResults } from './explore-logs.js';
import { MetricResults } from './explore-metrics.js';
import { TraceLookup, TraceResults } from './explore-traces.js';
import { MetricsOverview } from './explore-overview.js';

export { TraceLookup } from './explore-traces.js';
export type { ExploreSignal, ExploreRange } from './explore-query.js';

/** Everything a link to an Explore view carries; the console keeps it in the URL. */
export interface ExploreState { signal: ExploreSignal; query?: string; language?: string; range: ExploreRange; trace?: string; limit?: number }
const defaultState: ExploreState = { signal: 'logs', range: '15m' };
const limits = [100, 500, 1000, 5000];
const metricFunctions: MetricFunction[] = ['raw', 'rate', 'sum', 'p50', 'p95', 'p99'];

export interface ExploreRun { response: QueryObsResponse; from: bigint; to: bigint; query: string; signal: ExploreSignal; language: string }

export function ExplorePanel({ mode, service, state: controlled, onStateChange, onNavigate, defaultSignal = 'logs' }: {
  mode: 'dark' | 'light'; service?: string; state?: ExploreState; onStateChange?: (state: ExploreState) => void; onNavigate?: (target: CorrelationTarget) => void; defaultSignal?: ExploreSignal;
}) {
  const client = useClient(ObsServiceClient), text = usePlatformText();
  const [local, setLocal] = useState<ExploreState>(controlled ?? { ...defaultState, signal: defaultSignal });
  const state = controlled ?? local;
  const update = (patch: Partial<ExploreState>) => { const next = { ...state, ...patch }; setLocal(next); onStateChange?.(next); };
  const capabilities = usePlatformQuery('obs:capabilities', (signal) => client.getObsCapabilities(create(GetObsCapabilitiesRequestSchema), { signal }));
  const selectors = usePlatformQuery(`obs:selectors:${service ?? ''}`, (signal) => service ? client.getObsSelectors(create(GetObsSelectorsRequestSchema, { service }), { signal }) : Promise.resolve(undefined));
  const capability = capabilities.value?.signals.find((item) => item.signal === signalIds[state.signal]);
  const languages = capability?.languages.flatMap((item) => languageNames[item] ? [languageNames[item]!] : []) ?? [];
  const language = state.language && languages.includes(state.language as QueryLanguage) ? state.language as QueryLanguage : languages[0];
  const fields = useMemo(() => capability?.sourceFields ?? {}, [capability]);
  const scoped = selectors.value?.sources?.selectors[0]?.resource['service.name'] ?? service;
  const [builder, setBuilder] = useState<QueryBuilder>({ service: scoped });
  useEffect(() => { if (scoped) setBuilder((old) => ({ ...old, service: scoped })); }, [scoped]);
  const [draft, setDraft] = useState<string>();
  const built = buildQuery(state.signal, language, builder, fields);
  const query = draft ?? state.query ?? built;
  const limit = state.limit ?? (state.signal === 'traces' ? 1000 : 500);
  const action = usePlatformAction<ExploreRun>();
  const window = () => { const to = nanosNow(); return { startUnixNano: to - BigInt(ranges[state.range]) * 1_000_000n, endUnixNano: to }; };
  const run = useCallback((expression = query) => {
    if (!language || !expression.trim()) return;
    const range = window(), signal = state.signal;
    setDraft(undefined); if (expression !== state.query) update({ query: expression });
    void action.run(async (abort) => ({
      response: await client.queryObs(create(QueryObsRequestSchema, { signal: signalIds[signal], language: languageIds[language], expression, range, limit: signal === 'metrics' ? 0 : limit, stepNanos: signal === 'metrics' ? stepFor(state.range, 240) : 0n }), { signal: abort }),
      from: range.startUnixNano, to: range.endUnixNano, query: expression, signal, language,
    }));
  }, [query, language, state, client, limit]); // eslint-disable-line react-hooks/exhaustive-deps -- user-triggered
  const changeBuilder = (patch: Partial<QueryBuilder>) => {
    const next = { ...builder, ...patch }; setBuilder(next);
    const expression = buildQuery(state.signal, language, next, fields); setDraft(undefined); update({ query: expression }); run(expression);
  };
  // Run once capabilities and the service scope are known, and on signal/range/limit changes.
  const ready = !!language && (!service || !selectors.loading), last = useRef('');
  useEffect(() => {
    const key = `${state.signal}:${state.range}:${language}:${limit}:${ready}`;
    if (ready && last.current !== key) { last.current = key; run(state.query ?? built); }
  }, [ready, state.signal, state.range, language, limit]); // eslint-disable-line react-hooks/exhaustive-deps
  const options = useFieldValues(state.signal, state.range);
  const openTrace = (id: string) => update({ trace: id });
  const navigate = (target: CorrelationTarget) => { if (target.signal === 'traces' && target.traceId) openTrace(target.traceId); else onNavigate?.(target); };
  const result = action.value;
  // Metrics without a chosen metric or a typed query show the generated dashboard.
  const overview = state.signal === 'metrics' && !builder.metric && !(draft ?? state.query)?.trim();
  const serviceField = state.signal === 'traces' ? fields['service.name'] ?? 'resource_attr:service.name' : state.signal === 'metrics' ? 'service.name' : fields['service.name'] ?? 'service.name';
  return <section aria-label={text('explore')} className="flex h-full min-h-0 flex-col gap-3">
    <div className="grid shrink-0 gap-2 rounded-lg border border-border bg-card p-2">
      <div className="flex flex-wrap items-center gap-2">
        <div className="inline-flex h-8 items-center gap-0.5 rounded-md border border-border bg-muted p-0.5" role="group" aria-label={text('signal')}>
          {(['logs', 'metrics', 'traces'] as const).map((signal) => { const Icon = signal === 'logs' ? FileText : signal === 'metrics' ? Activity : Waypoints;
            return <button key={signal} type="button" aria-pressed={state.signal === signal} onClick={() => { setDraft(undefined); setBuilder({ service: builder.service }); update({ signal, query: undefined, language: undefined, limit: undefined }); }}
              className="inline-flex h-6 items-center gap-1.5 rounded-sm px-2.5 text-xs text-muted-foreground hover:text-foreground aria-pressed:bg-raised aria-pressed:text-foreground"><Icon className="size-3.5" />{text(signal)}</button>; })}
        </div>
        {languages.length > 1 ? <SelectControl className="h-8 w-32" aria-label={text('language')} value={language} onValueChange={(value) => { setDraft(undefined); update({ language: value, query: undefined }); }}
          options={languages.map((item) => ({ value: item, label: languageLabel[item] }))} />
          : language && <Badge variant="outline" className="h-8 rounded-md px-2.5 font-mono">{languageLabel[language]}</Badge>}
        <span className="mx-1 h-5 w-px bg-border" aria-hidden="true" />
        <FilterCombo mono label={text('service')} value={builder.service ?? ''} onChange={(value) => changeBuilder({ service: value || undefined })} loadOptions={options(serviceField)} />
        {state.signal === 'logs' && <>
          <FilterCombo label={text('level')} value={builder.level ?? ''} onChange={(value) => changeBuilder({ level: value || undefined })} loadOptions={options('severity_text')} />
          <SearchText value={builder.text ?? ''} onCommit={(value) => changeBuilder({ text: value || undefined })} />
        </>}
        {state.signal === 'metrics' && <>
          <Button size="sm" variant={overview ? 'secondary' : 'outline'} aria-pressed={overview} onClick={() => { setDraft(undefined); setBuilder({ service: builder.service }); update({ query: undefined }); }}><LayoutDashboard />{text('dashboard')}</Button>
          <FilterCombo mono className="max-w-96" label={text('metric')} value={builder.metric ?? ''} onChange={(metric) => changeBuilder({ metric: metric || undefined, fn: metric ? defaultFunction(metric) : undefined })} loadOptions={options('__name__', builder.service ? `{service.name=${JSON.stringify(builder.service)}}` : '')} />
          <SelectControl className="h-8 w-28" aria-label={text('aggregation')} value={builder.fn ?? (builder.metric ? defaultFunction(builder.metric) : 'raw')} onValueChange={(value) => changeBuilder({ fn: value as MetricFunction })}
            options={metricFunctions.map((fn) => ({ value: fn, label: text(`fn_${fn}`) }))} />
          <FilterCombo mono label={text('groupBy')} value={builder.groupBy ?? ''} onChange={(value) => changeBuilder({ groupBy: value || undefined })} loadOptions={options('')} options={['service.name', 'service.instance.id']} />
        </>}
        {state.signal === 'traces' && <>
          <FilterCombo mono label={text('span')} value={builder.span ?? ''} onChange={(value) => changeBuilder({ span: value || undefined })} loadOptions={options('name')} />
          <label className="inline-flex h-8 items-center gap-2 rounded-md border border-border px-2 text-xs text-muted-foreground"><Switch size="sm" checked={!!builder.errors} onCheckedChange={(errors) => changeBuilder({ errors })} />{text('errorsOnly')}</label>
        </>}
        <div className="ml-auto flex items-center gap-2">
          {state.signal !== 'metrics' && <SelectControl className="h-8 w-28" aria-label={text('limit')} value={String(limit)} onValueChange={(value) => update({ limit: Number(value) })}
            options={limits.filter((value) => value <= (capabilities.value?.maxLimit ?? 5000)).map((value) => ({ value: String(value), label: text('limitRows', { count: value }) }))} />}
          <SelectControl className="h-8 w-32" aria-label={text('timeRange')} value={state.range} onValueChange={(value) => update({ range: value as ExploreRange })}
            options={Object.keys(ranges).map((range) => ({ value: range, label: text('lastRange', { range }) }))} />
          <Button size="sm" disabled={!language || action.pending || action.disabled} onClick={() => run()}><Play />{action.pending ? text('running') : text('runQuery')}</Button>
        </div>
      </div>
      {language ? <CodeEditor label={text('expression')} mode={mode} value={query} onChange={setDraft} language={language} onSubmit={() => run()} height={56} />
        : capabilities.loading ? <Skeleton className="h-14" /> : <p className="m-0 px-1 text-sm text-muted-foreground" role="status">{text('noCapability')}</p>}
    </div>
    <div className="grid min-h-0 flex-1 gap-3 xl:grid-rows-[minmax(0,1fr)] xl:grid-cols-[240px_minmax(0,1fr)]">
      <FieldBrowser signal={state.signal} range={state.range} onFilter={(field, value) => {
        if (field === serviceField) changeBuilder({ service: value });
        else if (state.signal === 'logs' && field === 'severity_text') changeBuilder({ level: value });
        else if (state.signal === 'traces' && field === 'name') changeBuilder({ span: value });
        else if (state.signal === 'metrics' && field === '__name__') changeBuilder({ metric: value, fn: defaultFunction(value) });
        else { const clause = language === 'logsql' ? `${field}:=${JSON.stringify(value)}` : `${field}=${JSON.stringify(value)}`; const next = language === 'logsql' ? `${query.trim() === '*' ? '' : `${query.trim()} `}${clause}` : query.replace(/}/, `, ${clause}}`); setDraft(next); run(next); }
      }} />
      <div className="flex min-h-0 min-w-0 flex-col gap-3">
        {action.error !== undefined && <div className="flex shrink-0 items-center gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive" role="alert"><CircleAlert className="size-4" />{text('queryFailed')}</div>}
        {result?.response.info && (result.response.info.partial || result.response.info.truncated || result.response.info.warnings.length > 0) && <div className="shrink-0 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning" role="status">
          {(result.response.info.partial || result.response.info.truncated) && <div>{text('partial')}</div>}{result.response.info.warnings.map((warning, index) => <div key={index}>{warning}</div>)}</div>}
        {overview ? <MetricsOverview service={builder.service} range={state.range} mode={mode} onOpen={(metric, fn) => changeBuilder({ metric, fn })} /> : <>
        {action.pending && !result && <Skeleton className="h-64 shrink-0" />}
        {result && <ObsResults run={result} mode={mode} onNavigate={navigate} limit={limit} />}
        {!result && !action.pending && action.error === undefined && <Panel fill><EmptyState icon={<Search />} title={text('runToSee')} /></Panel>}
        </>}
      </div>
    </div>
    <DetailDrawer open={!!state.trace} onOpenChange={(open) => { if (!open) update({ trace: undefined }); }} size="full" title={<span className="font-mono">{state.trace}</span>} description={text('traceDetail')}>
      {state.trace && <TraceLookup traceId={state.trace} mode={mode} onNavigate={(target) => { update({ trace: undefined }); navigate(target); }} />}
    </DetailDrawer>
  </section>;
}

function SearchText({ value, onCommit }: { value: string; onCommit: (value: string) => void }) {
  const text = usePlatformText(), [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  return <form className="relative" onSubmit={(event) => { event.preventDefault(); onCommit(draft.trim()); }}>
    <Search className="pointer-events-none absolute top-2 left-2 size-3.5 text-muted-foreground" />
    <Input className="h-8 w-56 pl-7 text-xs" aria-label={text('containsText')} placeholder={text('containsText')} value={draft} onChange={(event) => setDraft(event.target.value)} onBlur={() => { if (draft.trim() !== value) onCommit(draft.trim()); }} />
  </form>;
}

/** Loaders of stored field values over the current window, for filter pickers. */
function useFieldValues(signal: ExploreSignal, range: ExploreRange) {
  const client = useClient(ObsServiceClient);
  return useCallback((field: string, filter = '') => async (abort: AbortSignal) => {
    const to = nanosNow(), window = { startUnixNano: to - BigInt(ranges[range]) * 1_000_000n, endUnixNano: to };
    if (!field) return (await client.listObsFields(create(ListObsFieldsRequestSchema, { signal: signalIds[signal], range: window, limit: 200 }), { signal: abort })).fields;
    if (field.endsWith('service.name') && signal !== 'metrics') {
      const sources = await client.listObsSources(create(ListObsSourcesRequestSchema, { signal: signalIds[signal], range: window, limit: 200 }), { signal: abort });
      return [...new Set(sources.sources.map((source) => source.resource['service.name']).filter((name): name is string => !!name))];
    }
    return (await client.listObsFieldValues(create(ListObsFieldValuesRequestSchema, { signal: signalIds[signal], range: window, field, filter, limit: 500 }), { signal: abort })).values;
  }, [client, signal, range]);
}

/** Stored fields and their top values; a click adds a filter. */
function FieldBrowser({ signal, range, onFilter }: { signal: ExploreSignal; range: ExploreRange; onFilter: (field: string, value: string) => void }) {
  const client = useClient(ObsServiceClient), text = usePlatformText();
  const [open, setOpen] = useState<string>(), [search, setSearch] = useState('');
  const window = () => { const to = nanosNow(); return { startUnixNano: to - BigInt(ranges[range]) * 1_000_000n, endUnixNano: to }; };
  const fields = usePlatformQuery(`obs:fields:${signal}:${range}`, (abort) => client.listObsFields(create(ListObsFieldsRequestSchema, { signal: signalIds[signal], range: window(), limit: 300 }), { signal: abort }));
  const values = usePlatformQuery(`obs:values:${signal}:${range}:${open}`, (abort) => open ? client.listObsFieldValues(create(ListObsFieldValuesRequestSchema, { signal: signalIds[signal], range: window(), field: open, limit: 50 }), { signal: abort }) : Promise.resolve(undefined));
  const visible = (fields.value?.fields ?? []).filter((field) => (field === '__name__' || !field.startsWith('_')) && field.toLowerCase().includes(search.toLowerCase()));
  return <Panel fill title={text('fields')} count={visible.length} flush>
    <div className="sticky top-0 z-[1] border-b border-border bg-card p-2"><Input className="h-7 text-xs" aria-label={text('filterFields')} placeholder={text('filterFields')} value={search} onChange={(event) => setSearch(event.target.value)} /></div>
    {fields.loading && !fields.value && <div className="grid gap-1 p-2">{[0, 1, 2, 3].map((key) => <Skeleton key={key} className="h-5" />)}</div>}
    {visible.map((field) => <div key={field} className="border-b border-border last:border-b-0">
      <button type="button" aria-expanded={open === field} onClick={() => setOpen(open === field ? undefined : field)} className="flex w-full items-center gap-1.5 px-3 py-1.5 text-left font-mono text-xs hover:bg-raised">
        <ChevronRight className={`size-3 shrink-0 text-muted-foreground transition-transform ${open === field ? 'rotate-90' : ''}`} /><span className="truncate" title={field}>{field}</span></button>
      {open === field && <div className="grid gap-0.5 pr-2 pb-1.5 pl-7">
        {values.loading && <Skeleton className="h-4" />}
        {values.value?.values.map((value) => <button key={value} type="button" onClick={() => onFilter(field, value)} title={text('addFilter')}
          className="truncate rounded-sm px-1.5 py-0.5 text-left font-mono text-2xs text-muted-foreground hover:bg-primary/10 hover:text-foreground">{value || '""'}</button>)}
        {values.value && !values.value.values.length && <span className="text-2xs text-muted-foreground">{text('noValues')}</span>}
      </div>}
    </div>)}
  </Panel>;
}

/** Results of one run, rendered by signal and result shape. */
export function ObsResults({ run, mode, onNavigate, limit }: { run: ExploreRun; mode: 'dark' | 'light'; onNavigate?: (target: CorrelationTarget) => void; limit: number }) {
  const text = usePlatformText(), result = run.response.result;
  if (result.case === 'rows' && run.signal === 'traces') return <TraceResults run={run} mode={mode} onOpen={(traceId) => onNavigate?.({ signal: 'traces', traceId })} />;
  if (result.case === 'rows') return <LogResults run={run} mode={mode} onNavigate={onNavigate} limit={limit} />;
  if (result.case === 'timeSeries') return <MetricResults run={run} mode={mode} />;
  if (result.case === 'traces') return <TraceResults run={run} mode={mode} onOpen={(traceId) => onNavigate?.({ signal: 'traces', traceId })} />;
  return <Panel fill><EmptyState icon={<Radio />} title={text('noResults')} /></Panel>;
}
