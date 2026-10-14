import { useCallback, useEffect, useMemo, useState } from 'react';
import { create } from '@bufbuild/protobuf';
import { ConfigServiceClient, WatchConfigRequestSchema, ListRevisionsRequestSchema, SaveRevisionRequestSchema, ValidateOverrideRequestSchema, RollbackRequestSchema, type ServiceConfig, type Revision, type ConfigViolation, type InstanceConfig } from '@backplane-labs/backplane-api';
import { ConfigSource, InstancePhase } from '@backplane-labs/backplane-api/backplanepb/v1/instance_pb';
import { useClient, useSnapshotWatch } from '@backplane-labs/backplane-react';
import { toNative, type Schema, type Schema_Field } from '@gopherex/schemapb';
import { fieldSchema } from '@backplane-labs/backplane-schema-forms';
import { Badge, Button, ConfirmAction, Count, DetailDrawer, EmptyState, Input, KeyValueList, SelectControl, Panel, Skeleton, StatusBadge, StatusDot, Switch, Timestamp } from '@backplane-labs/backplane-ui';
import { CodeEditor, DiffViewer } from '@backplane-labs/backplane-editors';
import { CircleAlert, CircleCheck, History, Lock, RotateCcw, SlidersHorizontal, Undo2 } from 'lucide-react';
import { usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { date, enumLabel } from './format.js';
import { formatDuration } from '@backplane-labs/backplane-ui';

export function ConfigurationPanel({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const client = useClient(ConfigServiceClient), text = usePlatformText();
  const state = useSnapshotWatch(useCallback((signal: AbortSignal) => client.watchConfig(create(WatchConfigRequestSchema, { service }), { signal }), [client, service]));
  if (!state.value?.config) return state.status === 'error'
    ? <div className="rounded-lg border border-border bg-card" role="alert"><EmptyState icon={<CircleAlert />} title={text('error')} /></div>
    : <div role="status" aria-label={text('loading')} className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_360px]"><Skeleton className="h-96" /><Skeleton className="h-96" /></div>;
  return <section aria-label={text('configuration')}>
    {state.status === 'stale' && <p className="mb-3 text-xs text-warning" role="status">{text('stale')}</p>}
    <ConfigDraft key={service} config={state.value.config} mode={mode} />
  </section>;
}

/** Resolves a dotted Live path to its schema field through objects and refs. */
function resolveField(schema: Schema | undefined, path: string): Schema_Field | undefined {
  let current = schema, field: Schema_Field | undefined;
  for (const segment of path.split('.')) {
    field = current?.fields.find((entry) => entry.name === segment);
    if (!field || !current) return undefined;
    current = fieldSchema(schema!, field, undefined) ?? undefined;
  }
  return field;
}

type Values = Record<string, string>;
const same = (a: Values, b: Values) => JSON.stringify(Object.entries(a).sort()) === JSON.stringify(Object.entries(b).sort());

function ConfigDraft({ config, mode }: { config: ServiceConfig; mode: 'dark' | 'light' }) {
  const client = useClient(ConfigServiceClient), text = usePlatformText(), action = usePlatformAction<unknown>();
  const [baseline, setBaseline] = useState(config.current), [values, setValues] = useState<Values>({ ...config.current?.values }), [comment, setComment] = useState('');
  const [violations, setViolations] = useState<ConfigViolation[]>(), [selected, setSelected] = useState<Revision>();
  const [before, setBefore] = useState(0n), [older, setOlder] = useState<Revision[]>([]);
  const revisions = usePlatformQuery(`revisions:${config.service}:${before}:${config.current?.revision}`, (signal) => client.listRevisions(create(ListRevisionsRequestSchema, { service: config.service, before, pageSize: 50 }), { signal }));
  useEffect(() => { setOlder([]); setBefore(0n); }, [config.current?.revision]);
  const dirty = !same(values, baseline?.values ?? {});
  const changed = new Set([...Object.keys(values), ...Object.keys(baseline?.values ?? {})].filter((key) => values[key] !== baseline?.values[key])).size;
  useEffect(() => { if (!dirty) return; const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; }; window.addEventListener('beforeunload', warn); return () => window.removeEventListener('beforeunload', warn); }, [dirty]);
  const write = (key: string, value: string | undefined) => { setViolations(undefined); setValues((old) => { const next = { ...old }; if (value === undefined) delete next[key]; else Object.defineProperty(next, key, { value, enumerable: true, configurable: true, writable: true }); return next; }); };
  const adopt = (revision?: Revision) => { if (revision) { setBaseline(revision); setValues({ ...revision.values }); setComment(''); revisions.refresh(); } };
  const newer = (config.current?.revision ?? 0n) > (baseline?.revision ?? 0n);
  const groups = useMemo(() => {
    const byGroup = new Map<string, string[]>();
    for (const path of config.live) { const group = path.includes('.') ? path.slice(0, path.indexOf('.')) : ''; byGroup.set(group, [...byGroup.get(group) ?? [], path]); }
    return [...byGroup.entries()];
  }, [config.live]);
  const pathViolations = (path: string) => violations?.filter((violation) => violation.path === path || violation.path.startsWith(`${path}.`) || violation.path.startsWith(`${path}[`)) ?? [];
  const general = violations?.filter((violation) => !config.live.some((path) => violation.path === path || violation.path.startsWith(`${path}.`) || violation.path.startsWith(`${path}[`))) ?? [];
  const busy = action.pending || action.disabled;
  const all = [...older, ...(revisions.value?.revisions ?? [])];
  return <div className="grid h-full min-h-0 gap-4 xl:grid-rows-[minmax(0,1fr)] xl:grid-cols-[minmax(0,1fr)_360px]">
    <div className="flex min-h-0 min-w-0 flex-col gap-4">
      {newer && <div className="flex items-center gap-2 rounded-lg border border-info/30 bg-info/10 px-3 py-2 text-sm text-info" role="status">
        <History className="size-4" />{text('newer')}<Button size="xs" variant="outline" className="ml-auto" onClick={() => adopt(config.current)}>{text('loadLatest')}</Button></div>}
      <Panel fill title={<><SlidersHorizontal className="size-4 text-muted-foreground" />{text('liveSettings')}</>} count={config.live.length} flush
        description={text('liveSettingsHelp')}
        actions={baseline && <span className="text-xs text-muted-foreground">{text('currentRevision', { revision: baseline.revision.toString() })} · <Timestamp value={date(baseline.createdAt)} /></span>}
        footer={<div className="flex w-full flex-wrap items-center gap-2">
          {dirty ? <Badge variant="outline" className="border-warning/40 text-warning">{text('changes', { count: changed })}</Badge> : <span>{text('noChanges')}</span>}
          {violations && !violations.length && <span className="inline-flex items-center gap-1 text-success" role="status"><CircleCheck className="size-3.5" />{text('valid')}</span>}
          {action.error !== undefined && <span className="text-destructive" role="alert">{text('mutationFailed')}</span>}
          <Input className="ml-auto h-7 w-full text-xs sm:w-64" aria-label={text('comment')} placeholder={text('commentPlaceholder')} value={comment} onChange={(event) => setComment(event.target.value)} disabled={busy} />
          <Button size="sm" variant="ghost" disabled={!dirty || action.pending} onClick={() => { setValues({ ...baseline?.values }); setViolations(undefined); }}><Undo2 />{text('reset')}</Button>
          <Button size="sm" variant="outline" disabled={busy} onClick={() => void action.run(async (signal) => {
            const response = await client.validateOverride(create(ValidateOverrideRequestSchema, { service: config.service, values }), { signal });
            if (!signal.aborted) setViolations(response.violations); return response;
          })}>{text('validate')}</Button>
          <Button size="sm" disabled={!dirty || busy} onClick={() => void action.run(async (signal) => {
            const response = await client.saveRevision(create(SaveRevisionRequestSchema, { service: config.service, values, comment }), { signal });
            if (!signal.aborted) { setViolations(response.violations); adopt(response.revision); } return response;
          })}>{action.pending ? text('pending') : text('saveRevision')}</Button>
        </div>}>
        {!config.live.length ? <EmptyState className="py-8" icon={<Lock />} title={text('noLive')} description={text('noLiveHelp')} /> : groups.map(([group, paths]) => <div key={group} className="border-b border-border last:border-b-0">
          {group && <div className="bg-muted px-3 py-1.5 text-2xs font-semibold tracking-wider text-muted-foreground uppercase">{group}</div>}
          {paths.map((path) => <LiveSetting key={path} path={path} field={resolveField(config.schema, path)} instances={config.instances} value={values[path]} committed={baseline?.values[path]}
            violations={pathViolations(path)} disabled={busy} mode={mode} onChange={(value) => write(path, value)} />)}
        </div>)}
        {general.length > 0 && <ul className="m-0 grid gap-1 border-t border-border px-3 py-2 text-xs text-destructive" role="alert">{general.map((violation, index) => <li key={index}>{violation.instance && <span className="font-mono">{violation.instance}: </span>}{violation.message}</li>)}</ul>}
      </Panel>
    </div>
    <aside className="flex min-h-0 min-w-0 flex-col gap-4">
      <Panel title={text('rollout')} count={config.instances.length} flush description={text('rolloutHelp')} maxBodyHeight={220}>
        {config.instances.length ? config.instances.map((instance) => <RolloutRow key={instance.id} instance={instance} current={config.current?.revision ?? 0n} />) : <EmptyState className="py-6" title={text('noInstances')} />}
      </Panel>
      <Panel fill title={<><History className="size-4 text-muted-foreground" />{text('revisions')}</>} count={all.length} flush
        footer={revisions.value?.nextBefore ? <Button size="xs" variant="ghost" disabled={revisions.loading} onClick={() => { setOlder(all); setBefore(revisions.value!.nextBefore); }}>{text('more')}</Button> : undefined}>
        {revisions.error !== undefined && <p className="m-0 px-3 py-2 text-xs text-destructive" role="alert">{text('error')}</p>}
        {!all.length && !revisions.loading && <EmptyState className="py-6" title={text('noRevisions')} />}
        <ol className="m-0 list-none p-0">{all.map((revision) => <li key={revision.revision.toString()} className="border-b border-border last:border-b-0">
          <button type="button" className="flex w-full items-start gap-2.5 px-3 py-2 text-left hover:bg-raised" onClick={() => setSelected(revision)}>
            <Count className="mt-0.5">#{revision.revision.toString()}</Count>
            <span className="min-w-0 flex-1"><span className="block truncate text-sm">{revision.comment || <span className="text-muted-foreground italic">{text('noComment')}</span>}</span>
              <span className="block truncate text-xs text-muted-foreground">{revision.author} · <Timestamp value={date(revision.createdAt)} />{revision.rollbackOf > 0n && ` · ${text('rollbackOf', { revision: revision.rollbackOf.toString() })}`}</span></span>
            {revision.revision === config.current?.revision && <StatusBadge tone="accent" dot={false}>{text('current')}</StatusBadge>}
          </button>
        </li>)}</ol>
      </Panel>
    </aside>
    <DetailDrawer open={!!selected} onOpenChange={(open) => { if (!open) setSelected(undefined); }} size="lg"
      title={selected && text('revisionTitle', { revision: selected.revision.toString() })} description={selected?.comment || undefined}
      actions={selected && selected.revision !== config.current?.revision && <ConfirmAction trigger={<><RotateCcw className="size-3.5" />{text('rollbackTo', { revision: selected.revision.toString() })}</>} title={text('rollbackConfirm')} description={text('rollbackHelp', { revision: selected.revision.toString() })} onConfirm={async (signal) => {
        const response = await client.rollback(create(RollbackRequestSchema, { service: config.service, revision: selected.revision, comment }), { signal });
        if (!signal.aborted) { setViolations(response.violations); adopt(response.revision); setSelected(undefined); }
      }} />}>
      {selected && <div className="grid gap-4">
        <KeyValueList items={[{ label: text('actor'), value: selected.author }, { label: text('time'), value: <Timestamp value={date(selected.createdAt)} absolute /> }, { label: text('overrides'), value: Object.keys(selected.values).length }]} />
        <div><div className="mb-1.5 text-xs text-muted-foreground">{text('diffAgainstDraft')}</div>
          <DiffViewer label={text('revisions')} before={readable(selected.values)} after={readable(values)} language="json" mode={mode} height={320} /></div>
      </div>}
    </DetailDrawer>
  </div>;
}

/** Override values are JSON texts; show them decoded and key-sorted. */
function readable(values: Values): string {
  return JSON.stringify(Object.fromEntries(Object.entries(values).sort(([a], [b]) => a.localeCompare(b)).map(([key, value]) => [key, parse(value) ?? value])), null, 2);
}

function RolloutRow({ instance, current }: { instance: InstanceConfig; current: bigint }) {
  const text = usePlatformText();
  const tone = instance.rejectedRevision > 0n ? 'danger' : instance.appliedRevision === current ? 'success' : 'warning';
  return <div className="grid gap-0.5 border-b border-border px-3 py-2 last:border-b-0">
    <div className="flex items-center gap-2"><StatusDot tone={tone} /><span className="min-w-0 flex-1 truncate font-mono text-xs">{instance.id}</span>
      <span className="text-2xs text-muted-foreground">{enumLabel(InstancePhase, instance.phase)}</span></div>
    <div className="pl-4 text-xs text-muted-foreground">{instance.rejectedRevision > 0n
      ? <span className="text-destructive">{text('revRejected', { revision: instance.rejectedRevision.toString() })}: {instance.error}</span>
      : instance.appliedRevision === current ? text('revApplied', { revision: instance.appliedRevision.toString() }) : text('pendingApply', { applied: instance.appliedRevision.toString(), current: current.toString() })}</div>
  </div>;
}

const sourceVariant = (source: ConfigSource) => source === ConfigSource.KV ? 'default' as const : 'outline' as const;

function LiveSetting({ path, field, instances, value, committed, violations, disabled, mode, onChange }: {
  path: string; field?: Schema_Field; instances: InstanceConfig[]; value?: string; committed?: string; violations: ConfigViolation[]; disabled: boolean; mode: 'dark' | 'light'; onChange: (value: string | undefined) => void;
}) {
  const text = usePlatformText(), overridden = value !== undefined, changed = value !== committed;
  const effective = instances.flatMap((instance) => instance.live.filter((live) => live.path === path));
  const distinct = [...new Set(effective.map((live) => live.value))];
  const inherited = distinct.length === 1 ? distinct[0] : undefined;
  const sources = [...new Set(effective.map((live) => live.source))];
  return <div className={`grid gap-2 px-3 py-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)] ${changed ? 'bg-warning/5' : ''}`}>
    <div className="min-w-0">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-sm font-medium">{field?.title || path.slice(path.lastIndexOf('.') + 1)}</span>
        {field?.secret && <Lock className="size-3 text-muted-foreground" aria-label={text('secret')} />}
        {changed && <span className="size-1.5 rounded-full bg-warning" aria-label={text('dirty')} />}
      </div>
      <div className="font-mono text-2xs text-muted-foreground">{path}{field?.unit && ` · ${field.unit}`}</div>
      {field?.description && <p className="m-0 mt-1 text-xs text-muted-foreground">{field.description}</p>}
      <div className="mt-1.5 flex flex-wrap items-center gap-1 text-2xs text-muted-foreground">
        {sources.map((source) => <Badge key={source} variant={sourceVariant(source)} className="h-4 px-1 text-2xs">{enumLabel(ConfigSource, source)}</Badge>)}
        <span className="truncate font-mono">{distinct.length > 1 ? text('varies') : inherited ? text('effectiveValue', { value: display(field, inherited) }) : ''}</span>
      </div>
    </div>
    <div className="grid min-w-0 content-start gap-1.5">
      <label className="flex items-center gap-2 text-xs text-muted-foreground">
        <Switch size="sm" checked={overridden} disabled={disabled} onCheckedChange={(checked) => onChange(checked ? seed(field, inherited) : undefined)} aria-label={`${text('override')} ${path}`} />
        {overridden ? text('overridden') : text('inherits')}
      </label>
      {overridden && <ValueEditor field={field} path={path} value={value} disabled={disabled} mode={mode} onChange={onChange} />}
      {violations.map((violation, index) => <p key={index} className="m-0 text-xs text-destructive" role="alert">{violation.instance && <span className="font-mono">{violation.instance}: </span>}{violation.message}</p>)}
    </div>
  </div>;
}

/** Durations arrive as integer nanoseconds in the effective configuration. */
function display(field: Schema_Field | undefined, value: string): string {
  const parsed = parse(value);
  if (field?.kind.case === 'duration' && typeof parsed === 'number') return formatDuration(parsed / 1e6);
  return value;
}

/** First override value: the effective one, in the form the override accepts. */
function seed(field: Schema_Field | undefined, inherited: string | undefined): string {
  if (!inherited) return initialFor(field);
  const parsed = parse(inherited);
  return field?.kind.case === 'duration' && typeof parsed === 'number' ? JSON.stringify(`${parsed / 1e6}ms`) : inherited;
}

function initialFor(field?: Schema_Field): string {
  switch (field?.kind.case) {
    case 'bool': return 'false';
    case 'int32': case 'int64': case 'uint32': case 'uint64': case 'float': case 'double': return '0';
    case 'string': case 'duration': case 'timestamp': return '""';
    case 'choice': { const option = field.kind.value.options[0]?.value; return option ? JSON.stringify(toNative(option)) : 'null'; }
    default: return 'null';
  }
}
function parse(value: string): unknown { try { return JSON.parse(value); } catch { return undefined; } }

function ValueEditor({ field, path, value, disabled, mode, onChange }: { field?: Schema_Field; path: string; value: string; disabled: boolean; mode: 'dark' | 'light'; onChange: (value: string) => void }) {
  const parsed = parse(value);
  switch (field?.kind.case) {
    case 'bool': return <label className="flex items-center gap-2 text-sm"><Switch checked={parsed === true} disabled={disabled} onCheckedChange={(checked) => onChange(String(checked))} aria-label={path} />{String(parsed === true)}</label>;
    case 'int32': case 'int64': case 'uint32': case 'uint64': case 'float': case 'double':
      return <Input className="h-8 font-mono" inputMode="decimal" aria-label={path} disabled={disabled} value={typeof parsed === 'number' || typeof parsed === 'string' ? String(parsed) : value}
        onChange={(event) => { const raw = event.target.value.trim(); onChange(/^-?\d+(\.\d+)?([eE][-+]?\d+)?$/.test(raw) ? raw : JSON.stringify(raw)); }} />;
    case 'string': case 'duration': case 'timestamp':
      return <Input className="h-8 font-mono" aria-label={path} disabled={disabled} type={field.secret ? 'password' : 'text'} autoComplete="off" value={typeof parsed === 'string' ? parsed : value} onChange={(event) => onChange(JSON.stringify(event.target.value))} />;
    case 'choice': {
      const options = field.kind.value.options.map((option) => ({ label: option.label || String(option.value ? toNative(option.value) : ''), value: option.value ? JSON.stringify(toNative(option.value)) : 'null' }));
      return <SelectControl className="h-8" aria-label={path} disabled={disabled} value={value} onValueChange={onChange}
        options={options.some((option) => option.value === value) ? options : [{ value, label: value }, ...options]} />;
    }
    default: return <CodeEditor label={path} language="json" mode={mode} value={value} onChange={onChange} height={96} disabled={disabled} />;
  }
}
