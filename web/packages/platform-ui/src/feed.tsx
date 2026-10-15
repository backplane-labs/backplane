import { useEffect, useRef, useState, type ReactNode } from 'react';
import type { JsonValue } from '@bufbuild/protobuf';
import { BarChart, type ChartSeries } from '@backplane-labs/backplane-charts';
import { Badge, Button, Combobox, Input, SelectControl, Panel, Popover, PopoverContent, PopoverTrigger, RefreshControl, Skeleton, TimeRangeControl,
  type TimeRangeValue } from '@backplane-labs/backplane-ui';
import { Plus, Search, X } from 'lucide-react';
import { usePlatformText } from './locales.js';

/**
 * Parts of a filterable feed page (audit, errors): chips of conditions with
 * a builder, text search, a time range with a refresh interval, a histogram
 * and value counts per field. A page names its targets (fields and
 * attributes) by opaque keys and maps chips to its own request.
 */

export type FeedOp = 'is' | 'is_not' | 'contains' | 'not_contains' | 'prefix' | 'exists' | 'not_exists' | 'gt' | 'gte' | 'lt' | 'lte';
/** One condition: a target's key, an operator and values (IS: any of). */
export interface FeedChip { key: string; op: FeedOp; values: JsonValue[] }
/** Something a chip can target. */
export interface FeedTarget { key: string; label: string; ops: readonly FeedOp[]; mono?: boolean }
/** A target's most frequent values. */
export interface FeedFacet { key: string; label: string; mono?: boolean; total?: number; values: readonly { value: JsonValue; count: number }[] }

export const textOps: readonly FeedOp[] = ['is', 'is_not', 'contains', 'not_contains', 'prefix', 'exists', 'not_exists'];
export const allOps: readonly FeedOp[] = [...textOps, 'gt', 'gte', 'lt', 'lte'];
export const takesValues = (op: FeedOp) => op !== 'exists' && op !== 'not_exists';
export const numeric = (op: FeedOp) => op === 'gt' || op === 'gte' || op === 'lt' || op === 'lte';
const negative = (op: FeedOp) => op === 'is_not' || op === 'not_contains' || op === 'not_exists';
export const valueLabel = (value: JsonValue) => typeof value === 'string' ? value : JSON.stringify(value);
const same = (a: JsonValue, b: JsonValue) => JSON.stringify(a) === JSON.stringify(b);

/** A typed value: JSON when it parses as a number, boolean or null, else the text. */
export function parseValue(text: string): JsonValue {
  const trimmed = text.trim();
  if (/^(-?\d+(\.\d+)?([eE][+-]?\d+)?|true|false|null)$/.test(trimmed)) return JSON.parse(trimmed) as JsonValue;
  return text;
}

/** The chips with value added to key's is (or is_not) chip: the one-click filter. */
export function addValue(chips: readonly FeedChip[], key: string, value: JsonValue, exclude = false): FeedChip[] {
  const op: FeedOp = exclude ? 'is_not' : 'is', at = chips.findIndex((chip) => chip.key === key && chip.op === op);
  if (at < 0) return [...chips, { key, op, values: [value] }];
  const chip = chips[at]!;
  if (chip.values.some((v) => same(v, value))) return [...chips];
  return chips.map((other, index) => index === at ? { ...chip, values: [...chip.values, value] } : other);
}

/** Whether a one-click is filter on key holds value. */
export const kept = (chips: readonly FeedChip[], key: string, value: JsonValue) =>
  chips.some((chip) => chip.key === key && chip.op === 'is' && chip.values.some((v) => same(v, value)));

/** The filter bar: pinned chips (not removable), chips, "+ Filter", text search and clear. */
export function FeedFilterBar({ chips, pinned, targets, suggest, onChips, text: value, onText }: {
  chips: readonly FeedChip[]; pinned?: ReactNode; targets: readonly FeedTarget[]; suggest: (key: string) => readonly JsonValue[];
  onChips: (chips: FeedChip[]) => void; text: string; onText: (text: string) => void;
}) {
  const text = usePlatformText(), [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  useEffect(() => { if (draft === value) return; const timer = setTimeout(() => onText(draft), 400); return () => clearTimeout(timer); }, [draft]); // eslint-disable-line react-hooks/exhaustive-deps
  return <div className="flex shrink-0 flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-2">
    {pinned}
    {chips.map((chip, index) => <ChipEditor key={index} chip={chip} targets={targets} suggest={suggest}
      onChange={(next) => onChips(chips.map((other, at) => at === index ? next : other))} onRemove={() => onChips(chips.filter((_, at) => at !== index))} />)}
    <ChipEditor targets={targets} suggest={suggest} onChange={(next) => onChips([...chips, next])} />
    <span className="relative ml-auto"><Search className="pointer-events-none absolute top-2 left-2 size-3.5 text-muted-foreground" />
      <Input className="h-8 w-56 pl-7 text-xs" aria-label={text('searchText')} placeholder={text('searchText')} value={draft} onChange={(event) => setDraft(event.target.value)} /></span>
    {(chips.length > 0 || value) && <Button size="sm" variant="ghost" onClick={() => { setDraft(''); onChips([]); onText(''); }}><X />{text('clearFilters')}</Button>}
  </div>;
}

/** The time range and the refresh interval (0: off). */
export function FeedTimeBar({ range, onRange, live, onLive, onRefresh, mode }: {
  range: TimeRangeValue; onRange: (range: TimeRangeValue) => void; live: number; onLive: (live: number) => void; onRefresh: () => void; mode: 'dark' | 'light';
}) {
  const text = usePlatformText();
  return <div className="flex shrink-0 flex-wrap items-center gap-3">
    <TimeRangeControl value={range} onChange={onRange} mode={mode} label={text('timeRange')} />
    <span className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">{text('liveRefresh')}
      <RefreshControl interval={live} onIntervalChange={onLive} onRefresh={onRefresh} /></span>
  </div>;
}

/** Counts over time; dragging selects a range. */
export function FeedHistogram({ title, count, series, loading, mode, onRange }: {
  title: string; count?: number; series?: readonly ChartSeries[]; loading: boolean; mode: 'dark' | 'light'; onRange: (from: number, to: number) => void;
}) {
  const text = usePlatformText();
  return <Panel className="shrink-0" title={title} count={count}>
    {series && series.some((entry) => entry.points.length)
      ? <BarChart label={title} mode={mode} height={110} controls={false} series={series} onRangeChange={({ from, to }) => onRange(from, to)} />
      : <div className="grid h-[110px] place-items-center text-xs text-muted-foreground">{loading ? <Skeleton className="h-full w-full" /> : text('noEntries')}</div>}
  </Panel>;
}

/** Value counts per target: click keeps a value, Alt+click excludes it. */
export function FeedFacets({ facets, chips, onAdd, decorate }: {
  facets: readonly FeedFacet[]; chips: readonly FeedChip[]; onAdd: (key: string, value: JsonValue, exclude: boolean) => void;
  decorate?: (key: string, value: JsonValue) => { prefix?: ReactNode; label?: string } | undefined;
}) {
  const text = usePlatformText();
  return <Panel fill flush title={text('auditFields')} className="min-h-0">
    <div className="grid gap-3 p-2">{facets.filter((facet) => facet.values.length).map((facet) => {
      const total = facet.total ?? facet.values.reduce((sum, entry) => sum + entry.count, 0);
      return <div key={facet.key} className="grid gap-0.5">
        <div className="flex items-baseline gap-1 px-1 text-2xs font-medium tracking-wide text-muted-foreground uppercase">
          <span className={facet.mono ? 'font-mono normal-case' : ''}>{facet.label}</span><span className="ml-auto font-normal">{total}</span></div>
        {facet.values.map((entry) => { const extra = decorate?.(facet.key, entry.value), share = total ? entry.count / total : 0;
          return <button key={JSON.stringify(entry.value)} type="button" title={text('facetHelp')} aria-pressed={kept(chips, facet.key, entry.value)} onClick={(event) => onAdd(facet.key, entry.value, event.altKey)}
            className="relative flex h-6 items-center gap-1.5 overflow-hidden rounded-sm px-1.5 text-left text-xs hover:bg-raised aria-pressed:bg-primary/10">
            <span className="absolute inset-y-0 left-0 bg-primary/10" style={{ width: `${Math.round(share * 100)}%` }} />
            {extra?.prefix}<span className="relative min-w-0 flex-1 truncate font-mono" title={valueLabel(entry.value)}>{extra?.label ?? valueLabel(entry.value)}</span>
            <span className="relative text-2xs text-muted-foreground tabular-nums">{entry.count}</span>
          </button>; })}
      </div>; })}</div>
  </Panel>;
}

/** A condition chip that edits in a popover; without a chip, "+ Filter" adds one. */
function ChipEditor({ chip, targets, suggest, onChange, onRemove }: {
  chip?: FeedChip; targets: readonly FeedTarget[]; suggest: (key: string) => readonly JsonValue[]; onChange: (chip: FeedChip) => void; onRemove?: () => void;
}) {
  const text = usePlatformText(), [open, setOpen] = useState(false), first = targets[0]?.key ?? '';
  const [key, setKey] = useState(chip?.key ?? first), [op, setOp] = useState<FeedOp>(chip?.op ?? 'is');
  const [values, setValues] = useState<JsonValue[]>(chip?.values ?? []), [draft, setDraft] = useState('');
  useEffect(() => { if (open) { setKey(chip?.key ?? first); setOp(chip?.op ?? 'is'); setValues(chip?.values ?? []); setDraft(''); } }, [open]); // eslint-disable-line react-hooks/exhaustive-deps
  const target = targets.find((entry) => entry.key === key), ops = target?.ops ?? textOps;
  const pending = draft.trim() ? [...values, numeric(op) ? Number(draft) : parseValue(draft)] : values;
  const ready = !takesValues(op) || (numeric(op) ? pending.length === 1 && typeof pending[0] === 'number' && !Number.isNaN(pending[0]) : pending.length > 0);
  const apply = () => { onChange({ key, op, values: takesValues(op) ? pending : [] }); setOpen(false); };
  const chipTarget = chip && targets.find((entry) => entry.key === chip.key);
  const title = chip && <><span className={chipTarget?.mono ? 'font-mono' : ''}>{chipTarget?.label ?? chip.key}</span>
    <span className="text-muted-foreground">{text(`auditOp_${chip.op}`)}</span>{takesValues(chip.op) && <span className="max-w-48 truncate font-mono">{chip.values.map(valueLabel).join(', ')}</span>}</>;
  return <Popover open={open} onOpenChange={setOpen}>
    {chip ? <Badge variant="outline" className={`h-7 gap-1.5 pr-0.5 text-xs ${negative(chip.op) ? 'border-destructive/40' : 'border-primary/40'}`}>
      <PopoverTrigger asChild><button type="button" className="inline-flex items-center gap-1.5">{title}</button></PopoverTrigger>
      <button type="button" aria-label={text('removeFilter', { name: chip.key })} className="grid size-5 place-items-center rounded-sm hover:bg-raised" onClick={onRemove}><X className="size-3" /></button></Badge>
      : <PopoverTrigger asChild><Button size="sm" variant="outline"><Plus />{text('newFilter')}</Button></PopoverTrigger>}
    <PopoverContent align="start" className="grid w-80 gap-3">
      <Combobox label={text('filterField')} options={targets.map((entry) => ({ value: entry.key, label: entry.label }))} values={[key]} onValuesChange={(next) => {
        const picked = next[0]; if (!picked) return;
        setKey(picked); setValues([]);
        const allowed = targets.find((entry) => entry.key === picked)?.ops ?? textOps;
        if (!allowed.includes(op)) setOp('is');
      }} />
      <SelectControl aria-label={text('filterOperator')} value={op} onValueChange={(value) => setOp(value as FeedOp)} options={ops.map((entry) => ({ value: entry, label: text(`auditOp_${entry}`) }))} />
      {takesValues(op) && <div className="grid gap-2">
        {values.length > 0 && <div className="flex flex-wrap gap-1">{values.map((value, at) => <Badge key={at} variant="outline" className="gap-1 font-mono">{valueLabel(value)}
          <button type="button" aria-label={text('removeFilter', { name: valueLabel(value) })} onClick={() => setValues(values.filter((_, other) => other !== at))}><X className="size-3" /></button></Badge>)}</div>}
        <Input aria-label={text('filterValue')} placeholder={text(numeric(op) ? 'filterNumber' : 'filterValueHint')} inputMode={numeric(op) ? 'decimal' : undefined} value={draft}
          onChange={(event) => setDraft(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); if (numeric(op) || !draft.trim()) { if (ready) apply(); } else { setValues(pending); setDraft(''); } } }} />
        {!numeric(op) && <Suggestions values={suggest(key).filter((value) => !values.some((v) => same(v, value)))} onPick={(value) => setValues([...values, value])} />}
      </div>}
      <div className="flex justify-end gap-2"><Button size="sm" variant="ghost" onClick={() => setOpen(false)}>{text('cancel')}</Button><Button size="sm" disabled={!ready} onClick={apply}>{text('applyFilter')}</Button></div>
    </PopoverContent>
  </Popover>;
}

function Suggestions({ values, onPick }: { values: readonly JsonValue[]; onPick: (value: JsonValue) => void }) {
  if (!values.length) return null;
  return <div className="flex max-h-32 flex-wrap gap-1 overflow-auto">{values.map((value) =>
    <button key={JSON.stringify(value)} type="button" className="h-6 rounded-sm border border-border px-2 font-mono text-2xs hover:border-border-strong" onClick={() => onPick(value)}>{valueLabel(value)}</button>)}</div>;
}

/** Ids that appeared since the feed (keyed by its filter) was first shown; each lights up for a moment. */
export function useFresh(ids: readonly string[] | undefined, key: string): ReadonlySet<string> {
  const seen = useRef<{ key: string; ids: Set<string> } | undefined>(undefined), [fresh, setFresh] = useState<ReadonlySet<string>>(new Set());
  useEffect(() => {
    if (!ids) return;
    if (seen.current?.key !== key) { seen.current = { key, ids: new Set(ids) }; return; }
    const added = ids.filter((id) => !seen.current!.ids.has(id));
    for (const id of ids) seen.current.ids.add(id);
    if (!added.length) return;
    setFresh(new Set(added));
    const timer = setTimeout(() => setFresh(new Set()), 2500);
    return () => clearTimeout(timer);
  }, [ids, key]);
  return fresh;
}
