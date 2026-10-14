import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { create } from '@bufbuild/protobuf';
import * as api from '@backplane-labs/backplane-api';
import { useClient, useSnapshotWatch } from '@backplane-labs/backplane-react';
import { Button, Combobox, DetailDrawer, EmptyState, Input, Panel, SectionLabel, Skeleton, StatRow, StatTile, StatusBadge, StatusDot } from '@backplane-labs/backplane-ui';
import { Blocks, Cable, ChevronRight, CircleAlert, Plus, Search, Zap } from 'lucide-react';
import { usePlatformText } from '../locales.js';
import { enumLabel } from '../format.js';
import { useWiringCatalog } from './catalog.js';
import { bindingTone, ruleTone, targetKey, WiringEditor, type DraftStore, type StoredDraft, type WiringTarget, type WiringView } from './editor.js';
import { activityDragType, serviceColor } from './graph.js';

export type { WiringTarget, WiringView } from './editor.js';

/** What the workspace shows; the host keeps it in the URL. */
export interface WiringState { target?: WiringTarget; view: WiringView }

/** backplane.v1.ActivityKind.WORKFLOW: the activity is a workflow, run as a child workflow. */
const activityKindWorkflow = 2;
const owner = (full: string) => full.slice(0, Math.max(0, full.indexOf('.'))) || full;

/** Drafts of every item opened in this workspace: switching items keeps them. */
function useDraftStore(): DraftStore & { keys: ReadonlySet<string> } {
  const drafts = useRef(new Map<string, StoredDraft>()), [keys, setKeys] = useState<ReadonlySet<string>>(new Set());
  const store = useMemo<DraftStore>(() => {
    const sync = () => setKeys((old) => { const next = new Set(drafts.current.keys()); return next.size === old.size && [...next].every((key) => old.has(key)) ? old : next; });
    return {
      get: (key) => drafts.current.get(key),
      set: (key, draft) => { drafts.current.set(key, draft); sync(); },
      delete: (key) => { if (drafts.current.delete(key)) sync(); },
    };
  }, []);
  return useMemo(() => ({ ...store, keys }), [store, keys]);
}

/**
 * The wiring Backplane configures between services: every hook's binding and
 * every rule, edited as YAML or as a graph against the live catalog.
 */
export function WiringWorkspace({ mode, state, onStateChange }: { mode: 'dark' | 'light'; state: WiringState; onStateChange: (state: WiringState) => void }) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText();
  const hookWatch = useSnapshotWatch(useCallback((signal: AbortSignal) => bindings.watchBindings(create(api.WatchBindingsRequestSchema), { signal }), [bindings]));
  const ruleWatch = useSnapshotWatch(useCallback((signal: AbortSignal) => rules.watchRules(create(api.WatchRulesRequestSchema), { signal }), [rules]));
  const catalog = useWiringCatalog(), drafts = useDraftStore();
  // Drafts live in this page: leaving it with unsaved ones asks first.
  const unsaved = drafts.keys.size > 0;
  useEffect(() => {
    if (!unsaved) return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [unsaved]);
  const [search, setSearch] = useState(''), [creating, setCreating] = useState<'binding' | 'rule'>(), [paletteOpen, setPaletteOpen] = useState(true);
  const hooks = hookWatch.value?.bindings ?? [], allRules = (ruleWatch.value?.rules ?? []).filter((rule) => !rule.current?.deleted);
  const deletedRules = (ruleWatch.value?.rules ?? []).filter((rule) => rule.current?.deleted);
  const [showDeleted, setShowDeleted] = useState(false);
  const query = search.trim().toLowerCase();
  const matches = (...values: (string | undefined)[]) => !query || values.some((value) => value?.toLowerCase().includes(query));
  const bindingGroups = group(hooks.filter((entry) => matches(entry.hook, entry.description)), (entry) => entry.service || owner(entry.hook));
  const ruleGroups = group(allRules.filter((rule) => matches(rule.current?.name, rule.current?.definition?.event, rule.id)), (rule) => owner(rule.current?.definition?.event ?? ''));
  const target = state.target, key = target && targetKey(target);
  const open = (next: WiringTarget) => onStateChange({ target: next, view: state.view === 'versions' || state.view === 'runs' || state.view === 'test' ? 'graph' : state.view });
  const hookInfo = target?.kind === 'binding' ? hooks.find((entry) => entry.hook === target.hook) : undefined;
  const ruleInfo = target?.kind === 'rule' ? allRules.find((rule) => rule.id === target.id) : undefined;
  const latest = hookInfo?.current?.version ?? ruleInfo?.current?.version;
  const itemClass = 'flex w-full items-center gap-2 px-3 py-1.5 text-left hover:bg-raised aria-pressed:bg-primary/10';
  const draftDot = (itemKey: string) => drafts.keys.has(itemKey) && <span className="size-1.5 shrink-0 rounded-full bg-warning" title={text('draft')} />;

  return <div className="grid h-full min-h-0 grid-cols-[300px_minmax(0,1fr)] gap-4">
    <div className="flex min-h-0 min-w-0 flex-col gap-3">
      <div className="grid gap-2">
        <span className="relative"><Search className="pointer-events-none absolute top-2 left-2 size-3.5 text-muted-foreground" />
          <Input className="h-8 pl-7 text-xs" aria-label={text('filterWiring')} placeholder={text('filterWiring')} value={search} onChange={(event) => setSearch(event.target.value)} /></span>
        <div className="grid grid-cols-2 gap-2">
          <Button size="sm" variant="outline" onClick={() => setCreating('binding')}><Plus />{text('newBinding')}</Button>
          <Button size="sm" variant="outline" onClick={() => setCreating('rule')}><Plus />{text('newRule')}</Button>
        </div>
      </div>
      <Panel fill flush className="min-h-0" title={<><Blocks className="size-4 text-muted-foreground" />{text('wiring')}</>} count={hooks.filter((entry) => entry.current && !entry.current.deleted).length + allRules.length}>
        {hookWatch.status === 'loading' && !hooks.length && <div className="grid gap-2 p-3">{[0, 1, 2].map((at) => <Skeleton key={at} className="h-7" />)}</div>}
        <SectionLabel className="sticky top-0 z-[1] flex items-center gap-1.5 border-b border-border bg-card px-3 py-1.5"><Cable className="size-3.5" />{text('bindings')}</SectionLabel>
        {!bindingGroups.length && <p className="m-0 px-3 py-2 text-xs text-muted-foreground">{text('noHooks')}</p>}
        {bindingGroups.map(([service, entries]) => <div key={`b:${service}`}>
          <div className="flex items-center gap-1.5 px-3 pt-2 pb-1 text-2xs font-medium tracking-wide text-muted-foreground uppercase"><span className="size-2 rounded-full" style={{ background: serviceColor(service) }} />{service}</div>
          {entries.map((entry) => { const itemKey = targetKey({ kind: 'binding', hook: entry.hook }); return <button key={entry.hook} type="button" aria-pressed={key === itemKey} className={itemClass} onClick={() => open({ kind: 'binding', hook: entry.hook })}>
            <span className="min-w-0 flex-1"><span className="block truncate font-mono text-xs">{entry.hook.slice(service.length + 1) || entry.hook}</span>
              <span className="block truncate text-2xs text-muted-foreground">{entry.current && !entry.current.deleted ? `v${entry.current.version} · ${text('steps', { count: Object.keys(entry.current.definition?.steps ?? {}).length })}` : entry.required ? text('requiredShort') : text('noBinding')}</span></span>
            {draftDot(itemKey)}<StatusBadge tone={bindingTone[entry.state]} className="shrink-0">{enumLabel(api.BindingState, entry.state)}</StatusBadge>
          </button>; })}
        </div>)}
        <SectionLabel className="sticky top-0 z-[1] mt-2 flex items-center gap-1.5 border-y border-border bg-card px-3 py-1.5"><Zap className="size-3.5" />{text('rules')}</SectionLabel>
        {!ruleGroups.length && <p className="m-0 px-3 py-2 text-xs text-muted-foreground">{ruleWatch.status === 'loading' ? text('loading') : text('noRules')}</p>}
        {ruleGroups.map(([service, entries]) => <div key={`r:${service}`}>
          <div className="flex items-center gap-1.5 px-3 pt-2 pb-1 text-2xs font-medium tracking-wide text-muted-foreground uppercase"><span className="size-2 rounded-full" style={{ background: serviceColor(service) }} />{service}</div>
          {entries.map((rule) => { const itemKey = targetKey({ kind: 'rule', id: rule.id }); return <button key={rule.id} type="button" aria-pressed={key === itemKey} className={itemClass} onClick={() => open({ kind: 'rule', id: rule.id })}>
            <span className="min-w-0 flex-1"><span className="block truncate text-xs">{rule.current?.name || rule.id}</span>
              <span className="block truncate font-mono text-2xs text-muted-foreground">{rule.current?.definition?.event.slice(service.length + 1)} · v{rule.current?.version.toString()}</span></span>
            {draftDot(itemKey)}<StatusBadge tone={ruleTone[rule.state]} className="shrink-0">{enumLabel(api.RuleState, rule.state)}</StatusBadge>
          </button>; })}
        </div>)}
        {!!deletedRules.length && <div className="border-t border-border">
          <button type="button" aria-expanded={showDeleted} onClick={() => setShowDeleted((value) => !value)} className="flex w-full items-center gap-1.5 px-3 py-1.5 text-2xs font-medium tracking-wide text-muted-foreground uppercase hover:text-foreground">
            <ChevronRight className={`size-3 transition-transform ${showDeleted ? 'rotate-90' : ''}`} />{text('deletedRules', { count: deletedRules.length })}</button>
          {showDeleted && deletedRules.map((rule) => { const itemKey = targetKey({ kind: 'rule', id: rule.id }); return <button key={rule.id} type="button" aria-pressed={key === itemKey} className={itemClass}
            onClick={() => onStateChange({ target: { kind: 'rule', id: rule.id }, view: 'versions' })} title={text('restoreHelp')}>
            <span className="min-w-0 flex-1"><span className="block truncate text-xs text-muted-foreground line-through">{rule.current?.name || rule.id}</span>
              <span className="block truncate font-mono text-2xs text-muted-foreground">v{rule.current?.version.toString()}</span></span>
          </button>; })}
        </div>}
      </Panel>
      <Palette open={paletteOpen} onOpenChange={setPaletteOpen} services={catalog.index.services} />
    </div>
    <div className="flex min-h-0 min-w-0 flex-col">
      {target && key ? <WiringEditor key={key} target={target} index={catalog.index} mode={mode} view={state.view} onView={(view) => onStateChange({ target, view })} drafts={drafts}
        hookInfo={hookInfo} latestVersion={latest} onSaved={(next) => onStateChange({ target: next, view: state.view })} onDeleted={() => onStateChange({ view: state.view })} />
        : <Overview hooks={hooks} rules={allRules} loading={hookWatch.status === 'loading'} onOpen={open} />}
    </div>
    <NewBinding open={creating === 'binding'} onOpenChange={(value) => setCreating(value ? 'binding' : undefined)} hooks={hooks}
      onPick={(hook, bound) => { setCreating(undefined); onStateChange({ target: { kind: 'binding', hook }, view: bound ? 'graph' : 'yaml' }); }} />
    <NewRule open={creating === 'rule'} onOpenChange={(value) => setCreating(value ? 'rule' : undefined)} events={[...catalog.index.events.keys()].sort()} onCreate={(event) => { setCreating(undefined); onStateChange({ target: { kind: 'rule', id: '', event }, view: 'yaml' }); }} />
  </div>;
}

function group<T>(items: readonly T[], by: (item: T) => string): [string, T[]][] {
  const out = new Map<string, T[]>();
  for (const item of items) out.set(by(item), [...out.get(by(item)) ?? [], item]);
  return [...out.entries()].sort(([a], [b]) => a.localeCompare(b));
}

/** Activities of every service, dragged onto the graph to add a step. */
function Palette({ open, onOpenChange, services }: { open: boolean; onOpenChange: (open: boolean) => void; services: readonly api.WiringContract[] }) {
  const text = usePlatformText(), [search, setSearch] = useState('');
  const query = search.trim().toLowerCase();
  const offered = services.map((service) => ({ service: service.service, activities: service.activities.filter((activity) => !query || `${service.service}.${activity.name}`.toLowerCase().includes(query)) })).filter((entry) => entry.activities.length);
  return <Panel flush className={open ? 'max-h-[40%] min-h-0' : undefined} title={<button type="button" className="flex items-center gap-1.5" aria-expanded={open} onClick={() => onOpenChange(!open)}>
    <ChevronRight className={`size-3.5 transition-transform ${open ? 'rotate-90' : ''}`} />{text('palette')}</button>} description={open ? text('paletteHelp') : undefined}
    count={services.reduce((sum, service) => sum + service.activities.length, 0)}>
    {open && <div className="flex min-h-0 flex-col">
      <div className="border-b border-border p-2"><Input className="h-7 text-xs" aria-label={text('filterActivities')} placeholder={text('filterActivities')} value={search} onChange={(event) => setSearch(event.target.value)} /></div>
      <div className="min-h-0 overflow-auto">{offered.map((entry) => <div key={entry.service}>
        <div className="flex items-center gap-1.5 px-3 pt-2 pb-1 text-2xs font-medium tracking-wide text-muted-foreground uppercase"><span className="size-2 rounded-full" style={{ background: serviceColor(entry.service) }} />{entry.service}</div>
        <ul className="m-0 list-none p-0" aria-label={entry.service}>{entry.activities.map((activity) => { const full = `${entry.service}.${activity.name}`; return <li key={full} draggable title={activity.description || full}
          onDragStart={(event) => { event.dataTransfer.setData(activityDragType, full); event.dataTransfer.setData('text/plain', full); event.dataTransfer.effectAllowed = 'copy'; }}
          className="mx-2 mb-1 flex cursor-grab items-center gap-2 rounded-md border border-border bg-background px-2 py-1 text-xs hover:border-border-strong active:cursor-grabbing" style={{ boxShadow: `inset 3px 0 0 ${serviceColor(entry.service)}` }}>
          <span className="min-w-0 flex-1 truncate font-mono">{activity.name}</span>
          {activity.kind === activityKindWorkflow && <span className="text-2xs text-muted-foreground">{text('workflow')}</span>}
        </li>; })}</ul>
      </div>)}{!offered.length && <p className="m-0 px-3 py-2 text-xs text-muted-foreground">{text('noActivities')}</p>}</div>
    </div>}
  </Panel>;
}

function Overview({ hooks, rules, loading, onOpen }: { hooks: api.HookBinding[]; rules: api.Rule[]; loading: boolean; onOpen: (target: WiringTarget) => void }) {
  const text = usePlatformText();
  const count = (state: api.BindingState) => hooks.filter((entry) => entry.state === state).length;
  const broken: { target: WiringTarget; title: ReactNode; violations: api.Violation[] }[] = [
    ...hooks.filter((entry) => entry.state === api.BindingState.BROKEN).map((entry) => ({ target: { kind: 'binding' as const, hook: entry.hook }, title: <span className="font-mono">{entry.hook}</span>, violations: entry.violations })),
    ...rules.filter((rule) => rule.state === api.RuleState.BROKEN).map((rule) => ({ target: { kind: 'rule' as const, id: rule.id }, title: rule.current?.name || rule.id, violations: rule.violations })),
  ];
  const required = hooks.filter((entry) => entry.state === api.BindingState.REQUIRED_UNBOUND);
  return <div className="flex min-h-0 flex-col gap-4 overflow-auto">
    <StatRow>
      <StatTile label={text('bound')} value={count(api.BindingState.BOUND)} icon={<Cable />} tone="success" />
      <StatTile label={text('requiredUnbound')} value={required.length} icon={<CircleAlert />} tone={required.length ? 'danger' : 'neutral'} />
      <StatTile label={text('brokenCount')} value={broken.length} icon={<CircleAlert />} tone={broken.length ? 'danger' : 'neutral'} />
      <StatTile label={text('rules')} value={rules.length} hint={text('pausedCount', { count: rules.filter((rule) => rule.paused).length })} icon={<Zap />} />
    </StatRow>
    {loading && !hooks.length ? <Skeleton className="h-40" /> : <>
      <Panel title={text('needsAttention')} count={broken.length + required.length} flush>
        {!broken.length && !required.length && <EmptyState className="py-8" icon={<Cable />} title={text('allWired')} description={text('allWiredHelp')} />}
        {broken.map((item, at) => <button key={`broken:${at}`} type="button" onClick={() => onOpen(item.target)} className="grid w-full gap-0.5 border-b border-border px-3 py-2 text-left hover:bg-raised">
          <span className="flex items-center gap-2 text-sm"><StatusDot tone="danger" />{item.title}<StatusBadge tone="danger" className="ml-auto">{text('broken')}</StatusBadge></span>
          {item.violations.slice(0, 2).map((violation, index) => <span key={index} className="truncate pl-4 text-xs text-muted-foreground"><span className="font-mono">{violation.path}</span> {violation.message}</span>)}
        </button>)}
        {required.map((entry) => <button key={entry.hook} type="button" onClick={() => onOpen({ kind: 'binding', hook: entry.hook })} className="flex w-full items-center gap-2 border-b border-border px-3 py-2 text-left text-sm hover:bg-raised">
          <StatusDot tone="danger" /><span className="font-mono">{entry.hook}</span><span className="truncate text-xs text-muted-foreground">{entry.description}</span><StatusBadge tone="danger" className="ml-auto">{text('requiredShort')}</StatusBadge>
        </button>)}
      </Panel>
      <p className="m-0 text-sm text-muted-foreground">{text('wiringHelp')}</p>
    </>}
  </div>;
}

/**
 * Bindings implement hooks services declare, so a new one starts from a hook:
 * those without a binding first (required ones on top); a bound hook opens
 * its binding.
 */
function NewBinding({ open, onOpenChange, hooks, onPick }: { open: boolean; onOpenChange: (open: boolean) => void; hooks: api.HookBinding[]; onPick: (hook: string, bound: boolean) => void }) {
  const text = usePlatformText();
  const isBound = (entry: api.HookBinding) => !!entry.current && !entry.current.deleted;
  const free = hooks.filter((entry) => !isBound(entry)).sort((a, b) => Number(b.required) - Number(a.required) || a.hook.localeCompare(b.hook));
  const bound = hooks.filter(isBound).sort((a, b) => a.hook.localeCompare(b.hook));
  const row = (entry: api.HookBinding) => <li key={entry.hook}><button type="button" onClick={() => onPick(entry.hook, isBound(entry))}
    className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left hover:bg-raised">
    <span className="min-w-0 flex-1"><span className="block truncate font-mono text-xs">{entry.hook}</span>
      {entry.description && <span className="block truncate text-2xs text-muted-foreground">{entry.description}</span>}</span>
    <StatusBadge tone={bindingTone[entry.state]} className="shrink-0">{enumLabel(api.BindingState, entry.state)}</StatusBadge>
  </button></li>;
  return <DetailDrawer open={open} onOpenChange={onOpenChange} title={text('newBinding')} description={text('newBindingHelp')}>
    <div className="grid gap-4">
      <div className="grid gap-1"><SectionLabel>{text('unboundHooks')}</SectionLabel>
        {free.length ? <ul className="m-0 grid list-none gap-0.5 p-0">{free.map(row)}</ul>
          : <p className="m-0 text-xs text-muted-foreground">{hooks.length ? text('allHooksBound') : text('noHooks')}</p>}</div>
      {!!bound.length && <div className="grid gap-1"><SectionLabel>{text('boundHooks')}</SectionLabel>
        <ul className="m-0 grid list-none gap-0.5 p-0">{bound.map(row)}</ul></div>}
    </div>
  </DetailDrawer>;
}

function NewRule({ open, onOpenChange, events, onCreate }: { open: boolean; onOpenChange: (open: boolean) => void; events: string[]; onCreate: (event: string) => void }) {
  const text = usePlatformText(), [event, setEvent] = useState<string>();
  return <DetailDrawer open={open} onOpenChange={onOpenChange} title={text('newRule')} description={text('newRuleHelp')}>
    <div className="grid gap-3">
      <Combobox label={text('event')} options={events.map((value) => ({ value, label: value }))} values={event ? [event] : []} onValuesChange={(values) => setEvent(values[0])} placeholder={text('pickEvent')} />
      {!events.length && <p className="m-0 text-xs text-muted-foreground">{text('noEvents')}</p>}
      <div><Button size="sm" disabled={!event} onClick={() => event && onCreate(event)}><Plus />{text('createRule')}</Button></div>
    </div>
  </DetailDrawer>;
}
