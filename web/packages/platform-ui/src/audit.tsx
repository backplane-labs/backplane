import { useCallback, useMemo, useState, type ReactNode } from 'react';
import { create, toJson, type JsonValue } from '@bufbuild/protobuf';
import { timestampDate, ValueSchema, type Value } from '@bufbuild/protobuf/wkt';
import { AuditHistogramRequestSchema, AuditFacetsRequestSchema, AuditFieldsRequestSchema, AuditServiceClient, AuditSource, SearchAuditRequestSchema,
  type AuditRecord } from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { Button, Count, DetailDrawer, EmptyState, Panel, Skeleton, StatusBadge, Timestamp, resolveTimeRange, type TimeRangeValue } from '@backplane-labs/backplane-ui';
import { JSONViewer } from '@backplane-labs/backplane-editors';
import { Boxes, ClipboardList, Minus, Plus, ShieldCheck, Waypoints, Workflow } from 'lucide-react';
import { usePlatformText } from './locales.js';
import { usePlatformQuery } from './runtime.js';
import { RunDrawer, type RunRef } from './runs.js';
import { serviceColor } from './wiring/graph.js';
import { addValue, allOps, FeedFacets, FeedFilterBar, FeedHistogram, FeedTimeBar, textOps, useFresh, valueLabel, type FeedChip, type FeedTarget } from './feed.js';
import { attributeKey, auditFields, conditionOf, detailFacts, fieldKey, filterOf, humanActor, keyOf, outcomeTone, type AuditChip, type AuditFieldName } from './audit-model.js';

/** What the audit page shows; the host keeps it in the URL. */
export interface AuditState { range: TimeRangeValue; chips: AuditChip[]; text: string; /** Refresh interval, ms; 0: off. */ live: number }
export const defaultAuditState: AuditState = { range: { from: 'now-24h', to: 'now', timeZone: 'browser' }, chips: [], text: '', live: 10_000 };

const facetFields: readonly AuditFieldName[] = ['source', 'service', 'action', 'outcome', 'actor'];
const hidden = (key: string) => key === 'backplane.audit' || key === 'backplane.ingest';
const jsonOf = (value?: Value): JsonValue => value ? toJson(ValueSchema, value) : null;

/**
 * The audit feed: platform control entries and application records in one
 * table, narrowed by conditions on fields and attributes, text and a time
 * range; a histogram over the range, value counts per field, a live refresh
 * and a record drawer. `service` pins a service condition.
 */
export function AuditExplorer({ mode, service, state, onStateChange, onOpenTrace }: {
  mode: 'dark' | 'light'; service?: string; state: AuditState; onStateChange: (state: AuditState) => void; onOpenTrace?: (traceId: string) => void;
}) {
  const text = usePlatformText(), client = useClient(AuditServiceClient);
  const [tick, setTick] = useState(0), [pages, setPages] = useState<string[]>([]), [selected, setSelected] = useState<string>(), [run, setRun] = useState<RunRef>();
  const chips = useMemo(() => service ? [{ key: fieldKey('service'), op: 'is' as const, values: [service] }, ...state.chips] : state.chips, [service, state.chips]);
  // The range resolves again on every refresh: "now-24h" moves with now.
  const range = useMemo(() => resolveTimeRange(state.range), [state.range, tick]); // eslint-disable-line react-hooks/exhaustive-deps
  const filter = useMemo(() => filterOf(chips, state.text, range), [chips, state.text, range]);
  const key = JSON.stringify([chips, state.text, state.range]);
  const setChips = (next: FeedChip[]) => { setPages([]); onStateChange({ ...state, chips: next }); };
  const add = (target: string, value: JsonValue, exclude = false) => setChips(addValue(state.chips, target, value, exclude));

  const feed = usePlatformQuery(`audit-feed:${key}:${tick}:${pages.join(',')}`, async (signal) => {
    const records: AuditRecord[] = [];
    let next = '';
    for (const cursor of ['', ...pages]) {
      const page = await client.searchAudit(create(SearchAuditRequestSchema, { filter, pageSize: 100, pageCursor: cursor }), { signal });
      records.push(...page.records); next = page.nextPageCursor;
    }
    return { records, next };
  });
  const histogram = usePlatformQuery(`audit-histogram:${key}:${tick}`, (signal) => client.auditHistogram(create(AuditHistogramRequestSchema, { filter, buckets: 60 }), { signal }));
  const fields = usePlatformQuery(`audit-fields:${key}:${tick}`, (signal) => client.auditFields(create(AuditFieldsRequestSchema, { filter }), { signal }));
  const attributeNames = useMemo(() => (fields.value?.fields ?? []).map((field) => field.attribute).filter((name) => !hidden(name)), [fields.value]);
  const facetKeys = useMemo(() => [...facetFields.map(fieldKey), ...attributeNames.slice(0, 6).map(attributeKey)], [attributeNames]);
  const facets = usePlatformQuery(`audit-facets:${key}:${tick}:${facetKeys.join(',')}`, (signal) =>
    client.auditFacets(create(AuditFacetsRequestSchema, { filter, targets: facetKeys.map(conditionOf), limit: 8 }), { signal }));
  const facetValues = useMemo(() => (facets.value?.facets ?? []).map((facet) => {
    const facetKey = keyOf(facet.target), field = facetKey.startsWith('field:') ? facetKey.slice(6) as AuditFieldName : undefined;
    return { key: facetKey, label: field ? text(`auditField_${field}`) : facetKey.slice(5), mono: !field, total: Number(facet.total),
      values: facet.values.map((entry) => ({ value: jsonOf(entry.value), count: Number(entry.count) })) };
  }), [facets.value, text]);
  const targets = useMemo<FeedTarget[]>(() => [...auditFields.map((field) => ({ key: fieldKey(field), label: text(`auditField_${field}`), ops: textOps })),
    ...attributeNames.map((name) => ({ key: attributeKey(name), label: name, ops: allOps, mono: true }))], [attributeNames, text]);

  const records = feed.value?.records ?? [];
  const fresh = useFresh(useMemo(() => feed.value?.records.map((record) => record.id), [feed.value]), key);
  const refresh = useCallback(() => setTick((value) => value + 1), []);
  const record = records.find((item) => item.id === selected);
  const time = (bucket: { start?: Parameters<typeof timestampDate>[0] }) => bucket.start ? timestampDate(bucket.start).getTime() : 0;

  return <section aria-label={text('audit')} className="flex h-full min-h-0 flex-col gap-3">
    <FeedFilterBar chips={state.chips} targets={targets} onChips={setChips} text={state.text} onText={(value) => onStateChange({ ...state, text: value })}
      suggest={(target) => facetValues.find((facet) => facet.key === target)?.values.map((entry) => entry.value) ?? []}
      pinned={service && <span className="inline-flex h-7 items-center rounded-md border border-border bg-raised px-2 font-mono text-xs">{text('serviceScope', { service })}</span>} />
    <FeedTimeBar range={state.range} onRange={(value) => onStateChange({ ...state, range: value })} live={state.live} onLive={(live) => onStateChange({ ...state, live })} onRefresh={refresh} mode={mode} />
    <FeedHistogram title={text('auditOverTime')} mode={mode} loading={histogram.loading}
      count={histogram.value?.buckets.reduce((sum, bucket) => sum + Number(bucket.platform + bucket.application), 0)}
      series={histogram.value && [
        { id: 'platform', label: text('platformAudit'), color: 'var(--chart-2)', points: histogram.value.buckets.map((bucket) => ({ x: time(bucket), value: bucket.platform })) },
        { id: 'application', label: text('applicationAudit'), color: 'var(--chart-1)', points: histogram.value.buckets.map((bucket) => ({ x: time(bucket), value: bucket.application })) },
        { id: 'failed', label: text('failedRecords'), color: 'var(--destructive)', points: histogram.value.buckets.map((bucket) => ({ x: time(bucket), value: bucket.failed })) },
      ]} onRange={(from, to) => onStateChange({ ...state, range: { ...state.range, from: new Date(from).toISOString(), to: new Date(to).toISOString() } })} />
    <div className="grid min-h-0 flex-1 grid-cols-[240px_minmax(0,1fr)] gap-3">
      <FeedFacets facets={facetValues} chips={chips} onAdd={add} decorate={(facetKey, value) => facetKey === fieldKey('service')
        ? { prefix: <span className="relative size-1.5 shrink-0 rounded-full" style={{ background: serviceColor(valueLabel(value)) }} /> }
        : facetKey === fieldKey('actor') ? { label: humanActor(valueLabel(value)) } : undefined} />
      <Panel fill flush title={<><ClipboardList className="size-4 text-muted-foreground" />{text('entries')}</>} count={records.length}
        footer={<><span>{text('newestFirst')}</span><Button size="xs" variant="ghost" className="ml-auto" disabled={!feed.value?.next || feed.loading} onClick={() => setPages([...pages, feed.value!.next])}>{feed.loading && pages.length ? text('loading') : text('more')}</Button></>}>
        {feed.loading && !records.length ? <div className="grid gap-1 p-3">{[0, 1, 2, 3, 4, 5].map((at) => <Skeleton key={at} className="h-8" />)}</div>
          : !records.length ? <EmptyState className="py-10" icon={<ClipboardList />} title={text('noEntries')} description={chips.length || state.text ? text('noEntriesFiltered') : text('auditEmptyHelp')} />
          : <table aria-label={text('audit')} className="w-full text-sm">
            <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-xs text-muted-foreground">
              {[text('time'), '', text('service'), text('action'), text('actor'), text('subject'), text('outcome'), text('detail'), ''].map((heading, at) => <th key={at} className="h-8 px-2 font-medium whitespace-nowrap">{heading}</th>)}</tr></thead>
            <tbody>{records.map((item) => <Row key={item.id} record={item} fresh={fresh.has(item.id)} selected={item.id === selected} onOpen={() => setSelected(item.id)}
              onAdd={add} onTrace={onOpenTrace} onRun={setRun} />)}</tbody>
          </table>}
      </Panel>
    </div>
    <DetailDrawer open={!!record} onOpenChange={(open) => { if (!open) setSelected(undefined); }} size="lg" title={record && <span className="font-mono">{record.action}</span>}
      description={record && <span className="font-mono">{record.service || '—'}{record.subject && ` · ${record.subject}`}</span>}
      actions={record && <>
        {record.traceId && onOpenTrace && <Button size="xs" variant="outline" onClick={() => { onOpenTrace(record.traceId); setSelected(undefined); }}><Waypoints />{text('openTrace')}</Button>}
        {typeof record.attributes?.workflow_id === 'string' && <Button size="xs" variant="outline" onClick={() => { setRun({ workflowId: String(record.attributes!.workflow_id), runId: String(record.attributes!.run_id ?? '') }); setSelected(undefined); }}><Workflow />{text('openRun')}</Button>}
        {record.operationId && <Button size="xs" variant="outline" onClick={() => { add(fieldKey('operation'), record.operationId); setSelected(undefined); }}>{text('filterOperation')}</Button>}</>}>
      {record && <RecordDetail record={record} mode={mode} onAdd={add} />}
    </DetailDrawer>
    <RunDrawer run={run} onClose={() => setRun(undefined)} mode={mode} />
  </section>;
}

function Row({ record, fresh, selected, onOpen, onAdd, onTrace, onRun }: {
  record: AuditRecord; fresh: boolean; selected: boolean; onOpen: () => void; onAdd: (key: string, value: JsonValue, exclude?: boolean) => void;
  onTrace?: (traceId: string) => void; onRun: (run: RunRef) => void;
}) {
  const text = usePlatformText(), platform = record.source === AuditSource.PLATFORM, facts = detailFacts(record);
  const workflow = typeof record.attributes?.workflow_id === 'string' ? String(record.attributes.workflow_id) : '';
  const cell = (field: AuditFieldName, value: string, children: ReactNode) => <button type="button" className="max-w-full truncate text-left hover:text-link" title={text('facetHelp')}
    onClick={(event) => { event.stopPropagation(); onAdd(fieldKey(field), value, event.altKey); }}>{children}</button>;
  return <tr tabIndex={0} aria-selected={selected} onClick={onOpen} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); onOpen(); } }}
    className={`cursor-pointer border-b border-border transition-colors duration-1000 last:border-b-0 hover:bg-raised focus-visible:bg-raised focus-visible:outline-none aria-selected:bg-primary/10 ${fresh ? 'bg-info/15' : ''}`}>
    <td className="h-9 px-2 text-xs whitespace-nowrap text-muted-foreground"><Timestamp value={record.time ? timestampDate(record.time) : undefined} /></td>
    <td className="px-1" title={text(platform ? 'platformAudit' : 'applicationAudit')}>{platform ? <ShieldCheck className="size-3.5 text-chart-2" aria-label={text('platformAudit')} /> : <Boxes className="size-3.5 text-chart-1" aria-label={text('applicationAudit')} />}</td>
    <td className="max-w-32 px-2 font-mono text-xs">{record.service ? cell('service', record.service, <span className="inline-flex items-center gap-1.5"><span className="size-1.5 shrink-0 rounded-full" style={{ background: serviceColor(record.service) }} />{record.service}</span>) : <span className="text-subtle">—</span>}</td>
    <td className="max-w-64 px-2">{cell('action', record.action, <span className="rounded-sm bg-raised px-1.5 py-0.5 font-mono text-xs">{record.action}</span>)}</td>
    <td className="max-w-40 px-2 font-mono text-xs" title={record.actor}>{record.actor ? cell('actor', record.actor, humanActor(record.actor)) : <span className="text-subtle">—</span>}</td>
    <td className="max-w-56 px-2 font-mono text-xs" title={record.subject}>{record.subject ? cell('subject', record.subject, record.subject) : <span className="text-subtle">—</span>}</td>
    <td className="px-2">{record.outcome ? <StatusBadge tone={outcomeTone(record.outcome)}>{record.outcome}</StatusBadge> : <span className="text-subtle">—</span>}</td>
    <td className="max-w-96 truncate px-2 text-xs text-muted-foreground" title={facts.join(' · ')}>{facts.join(' · ') || '—'}</td>
    <td className="px-2 whitespace-nowrap">
      {record.traceId && onTrace && <Button size="icon-sm" variant="ghost" aria-label={text('openTrace')} title={text('openTrace')} onClick={(event) => { event.stopPropagation(); onTrace(record.traceId); }}><Waypoints /></Button>}
      {workflow && <Button size="icon-sm" variant="ghost" aria-label={text('openRun')} title={text('openRun')} onClick={(event) => { event.stopPropagation(); onRun({ workflowId: workflow, runId: String(record.attributes?.run_id ?? '') }); }}><Workflow /></Button>}
    </td>
  </tr>;
}

/** Every field and attribute of a record, each with keep/exclude filters. */
function RecordDetail({ record, mode, onAdd }: { record: AuditRecord; mode: 'dark' | 'light'; onAdd: (key: string, value: JsonValue, exclude: boolean) => void }) {
  const text = usePlatformText();
  const fields: [string, string, string][] = ([['source', record.source === AuditSource.PLATFORM ? 'platform' : 'application'], ['service', record.service], ['action', record.action],
    ['actor', record.actor], ['subject', record.subject], ['outcome', record.outcome], ['operation', record.operationId], ['severity', record.severity], ['trace_id', record.traceId]] as const)
    .filter(([, value]) => value).map(([field, value]) => [fieldKey(field), text(`auditField_${field}`), value]);
  const attributes = Object.entries((record.attributes ?? {}) as Record<string, JsonValue>).sort(([a], [b]) => a.localeCompare(b));
  return <div className="grid gap-4">
    <div className="flex flex-wrap gap-4 text-xs text-muted-foreground">
      <span>{text('time')}: <Timestamp absolute value={record.time ? timestampDate(record.time) : undefined} className="text-foreground" /></span>
      <span>{text('receivedAt')}: <Timestamp absolute value={record.receivedAt ? timestampDate(record.receivedAt) : undefined} className="text-foreground" /></span>
      {record.sequence > 0n && <span>#{record.sequence.toString()}</span>}
    </div>
    {record.message && <p className="m-0 rounded-md border border-border bg-raised px-3 py-2 font-mono text-xs break-all">{record.message}</p>}
    <table className="w-full"><tbody>
      {fields.map(([key, name, value]) => <FilterRow key={key} filterKey={key} name={name} value={value} onAdd={onAdd} />)}
      {attributes.map(([name, value]) => <FilterRow key={name} filterKey={attributeKey(name)} name={<span className="font-mono">{name}</span>} value={value} onAdd={onAdd} />)}
    </tbody></table>
    {record.resource && Object.keys(record.resource).length > 0 && <div><div className="mb-1.5 flex items-center gap-2 text-xs text-muted-foreground">{text('resource')}<Count>JSON</Count></div>
      <JSONViewer label={text('resource')} value={JSON.stringify(record.resource, null, 2)} mode={mode} height={160} /></div>}
  </div>;
}

/** A name, a value and keep/exclude buttons. */
export function FilterRow({ filterKey, name, value, onAdd }: { filterKey: string; name: ReactNode; value: JsonValue; onAdd: (key: string, value: JsonValue, exclude: boolean) => void }) {
  const text = usePlatformText();
  return <tr className="border-b border-border last:border-b-0">
    <td className="py-1 pr-3 align-top text-xs whitespace-nowrap text-muted-foreground">{name}</td>
    <td className="py-1 font-mono text-xs break-all">{valueLabel(value)}</td>
    <td className="w-14 py-1 text-right whitespace-nowrap">
      <Button size="icon-sm" variant="ghost" aria-label={text('filterFor', { name: filterKey })} title={text('filterFor', { name: filterKey })} onClick={() => onAdd(filterKey, value, false)}><Plus /></Button>
      <Button size="icon-sm" variant="ghost" aria-label={text('filterOut', { name: filterKey })} title={text('filterOut', { name: filterKey })} onClick={() => onAdd(filterKey, value, true)}><Minus /></Button></td>
  </tr>;
}
