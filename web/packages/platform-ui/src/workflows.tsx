import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { create, fromJsonString, toJsonString, type DescMessage, type MessageShape } from '@bufbuild/protobuf';
import { DurationSchema, type Duration } from '@bufbuild/protobuf/wkt';
import * as api from '@backplane-labs/backplane-api';
import { structToNative } from '@gopherex/schemapb';
import { ScheduleOverlap } from '@backplane-labs/backplane-api/backplanepb/v1/manifest_pb';
import { useClient } from '@backplane-labs/backplane-react';
import { WsStatusError } from '@backplane-labs/backplane-client';
import { SchemaForm } from '@backplane-labs/backplane-schema-forms';
import { Badge, Button, ConfirmAction, DetailDrawer, EmptyState, FilterCombo, Input, KeyValueList, Panel, StatusBadge, Timestamp, ViewToggle } from '@backplane-labs/backplane-ui';
import { CodeEditor, JSONViewer } from '@backplane-labs/backplane-editors';
import { CalendarClock, ChevronDown, ChevronRight, History, Pause, Play, RefreshCw, TriangleAlert, Workflow, Zap } from 'lucide-react';
import { MutationState, usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { date, durationText, enumLabel } from './format.js';
import { nativeJSON } from './serialization.js';
import { ScheduleEditor } from './schedule-admin.js';
import { RunDrawer, RunPager, RunStatusFilter, RunTable, useRunPages, type RunRef } from './runs.js';

/** Generic protobuf-JSON command with confirmation (technical fallback surfaces). */
export function CommandForm<S extends DescMessage>({ label, schema, initialValue, execute, mode, onComplete }: {
  label: string; schema: S; initialValue: MessageShape<S>; execute: (value: MessageShape<S>, signal: AbortSignal) => Promise<string>;
  mode: 'dark' | 'light'; onComplete?: () => void;
}) {
  const text = usePlatformText(), [value, setValue] = useState(toJsonString(schema, initialValue, { prettySpaces: 2 })), action = usePlatformAction<string>();
  return <section aria-label={label} className="grid gap-2"><CodeEditor label={label} mode={mode} language="json" value={value} onChange={setValue} disabled={action.pending} />
    <div><ConfirmAction trigger={label} title={text('confirmTitle')} description={text('confirmDescription')} disabled={action.pending || action.disabled} onConfirm={async (signal) => {
      const result = await action.run((abort) => execute(fromJsonString(schema, value), AbortSignal.any([signal, abort])));
      if (result === undefined) throw new Error('Action outcome is unavailable'); onComplete?.();
    }} /></div><MutationState action={action} />{action.value !== undefined && <JSONViewer label={text('output')} value={action.value} mode={mode} />}</section>;
}

type RunKind = 'workflows' | 'hooks';
const distinct = (values: string[]) => [...new Set(values.filter(Boolean))];

/** Workflow runs of a service — or its hook calls — with workflow, status and id-prefix filters, paging and a run drawer. `reload` changing refreshes the list. */
export function WorkflowRuns(props: { service: string; mode: 'dark' | 'light'; workflow?: string; prefix?: string; onPrefixChange?: (prefix: string) => void; reload?: number }) {
  return <ServiceRuns key={`${props.service}:${props.workflow ?? ''}:${props.prefix ?? ''}`} {...props} />;
}
function ServiceRuns({ service, mode, workflow: preset = '', prefix: presetPrefix = '', onPrefixChange, reload = 0 }: { service: string; mode: 'dark' | 'light'; workflow?: string; prefix?: string; onPrefixChange?: (prefix: string) => void; reload?: number }) {
  const client = useClient(api.WorkflowServiceClient), schedules = useClient(api.ScheduleServiceClient), text = usePlatformText();
  const [kind, setKind] = useState<RunKind>('workflows'), [workflow, setWorkflow] = useState(preset), [prefix, setPrefix] = useState(presetPrefix);
  const loadWorkflows = useCallback(async (signal: AbortSignal) => (await client.listWorkflows(create(api.ListWorkflowsRequestSchema, { service }), { signal })).workflows.map((entry) => entry.name), [client, service]);
  const loadPrefixes = useCallback(async (signal: AbortSignal) => (await schedules.listSchedules(create(api.ListSchedulesRequestSchema, { service }), { signal })).schedules.map((entry) => ({ value: `${entry.id}-`, hint: entry.name })), [schedules, service]);
  const [status, setStatus] = useState(api.RunStatus.UNSPECIFIED), pages = useRunPages(), [selected, setSelected] = useState<RunRef>();
  const hooks = kind === 'hooks', first = pages.first;
  const state = usePlatformQuery(`runs:${service}:${kind}:${workflow}:${status}:${prefix}:${Array.from(pages.page)}`, (signal) => client.listRuns(create(api.ListRunsRequestSchema, { service, hooks, workflow, status, workflowIdPrefix: prefix, pageToken: pages.page, pageSize: 50 }), { signal }));
  const refresh = state.refresh, seen = useRef(reload);
  useEffect(() => { if (seen.current !== reload) { seen.current = reload; refresh(); } }, [reload, refresh]);
  return <Panel fill title={<><Workflow className="size-4 text-muted-foreground" />{text('runs')}</>} count={state.value?.runs.length} flush
    actions={<><ViewToggle label={text('runKind')} value={kind} onValueChange={(value) => { setKind(value); setWorkflow(''); first(); }} options={[{ value: 'workflows', label: text('workflowRuns') }, { value: 'hooks', label: text('hookCalls') }]} />
      <Button size="icon-sm" variant="ghost" aria-label={text('refresh')} title={text('refresh')} onClick={state.refresh} disabled={state.loading}><RefreshCw className={state.loading ? 'animate-spin' : ''} /></Button></>}
    footer={<RunPager pages={pages} next={state.value?.nextPageToken} loading={state.loading} />}>
    <div className="sticky top-0 z-[2] flex flex-wrap items-center gap-2 border-b border-border bg-card px-3 py-2">
      <FilterCombo mono label={text('workflow')} value={workflow} onChange={(value) => { setWorkflow(value); first(); }} loadOptions={hooks ? undefined : loadWorkflows} options={distinct((state.value?.runs ?? []).map((run) => run.workflowType))} />
      <FilterCombo mono label={text('idPrefix')} value={prefix} onChange={(value) => { setPrefix(value); first(); onPrefixChange?.(value); }} loadOptions={hooks ? undefined : loadPrefixes} />
      <RunStatusFilter value={status} onChange={(value) => { setStatus(value); first(); }} /></div>
    {state.error !== undefined && <p className="m-0 px-3 py-2 text-xs text-destructive" role="alert">{text('error')}</p>}
    <RunTable runs={state.value?.runs ?? []} loading={state.loading} onOpen={setSelected} />
    <RunDrawer run={selected} onClose={() => setSelected(undefined)} mode={mode} />
  </Panel>;
}

/** Stand-alone run detail (used by module authors and stories). */
export function RunInspector({ workflowId, runId, mode }: { workflowId: string; runId: string; mode: 'dark' | 'light' }) {
  const [open, setOpen] = useState(true);
  return <>{!open && <Button variant="outline" size="sm" onClick={() => setOpen(true)}>{workflowId}</Button>}<RunDrawer run={open ? { workflowId, runId } : undefined} onClose={() => setOpen(false)} mode={mode} /></>;
}

/** Declared workflows with start-from-schema, then runs and schedules. */
export function WorkflowsPanel({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const client = useClient(api.WorkflowServiceClient), text = usePlatformText();
  const definitions = usePlatformQuery(`workflows:${service}`, (signal) => client.listWorkflows(create(api.ListWorkflowsRequestSchema, { service }), { signal }));
  const [starting, setStarting] = useState<api.WorkflowDef>(), [filter, setFilter] = useState<string>(), [prefix, setPrefix] = useState(''), [reload, setReload] = useState(0);
  const workflows = definitions.value?.workflows ?? [], idle = distinct(workflows.filter((workflow) => workflow.pollers === 0).map((workflow) => workflow.taskQueue));
  const changed = () => setReload((value) => value + 1);
  return <div className="grid h-full min-h-0 gap-4 xl:grid-rows-[minmax(0,1fr)] xl:grid-cols-[340px_minmax(0,1fr)]">
    <div className="flex min-h-0 min-w-0 flex-col gap-4 overflow-auto">
      {idle.map((queue) => <p key={queue} className="m-0 flex items-start gap-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning" role="alert"><TriangleAlert className="mt-0.5 size-3.5 shrink-0" />{text('noPollers', { queue })}</p>)}
      <Panel title={text('workflows')} count={workflows.length} flush maxBodyHeight={280}>
        {!workflows.length && !definitions.loading && <EmptyState className="py-6" title={text('noWorkflows')} />}
        {workflows.map((workflow) => <div key={workflow.name} className={`group flex items-center gap-2 border-b border-border px-3 py-2 last:border-b-0 ${filter === workflow.name ? 'bg-primary/10' : ''}`}>
          <button type="button" className="min-w-0 flex-1 text-left" onClick={() => setFilter(filter === workflow.name ? undefined : workflow.name)} aria-pressed={filter === workflow.name}>
            <span className="flex items-center gap-1.5"><span className="truncate font-mono text-xs">{workflow.name}</span>{workflow.kind === api.WorkflowKind.ACTIVITY && <Badge variant="outline">{text('activity')}</Badge>}</span>
            {workflow.description && <span className="block truncate text-xs text-muted-foreground">{workflow.description}</span>}
          </button>
          <Button size="xs" variant="outline" aria-label={text('startWorkflow', { name: workflow.name })} onClick={() => setStarting(workflow)}><Play />{text('startAction')}</Button>
        </div>)}
      </Panel>
      <SchedulesPanel service={service} mode={mode} onShowRuns={setPrefix} onTriggered={changed} />
    </div>
    <WorkflowRuns service={service} mode={mode} workflow={filter} prefix={prefix} onPrefixChange={setPrefix} reload={reload} />
    <DetailDrawer open={!!starting} onOpenChange={(open) => { if (!open) setStarting(undefined); }} title={text('startWorkflow', { name: starting?.name ?? '' })} description={starting?.description}>
      {starting && <StartWorkflow key={starting.name} service={service} workflow={starting} mode={mode} onStarted={changed} />}
    </DetailDrawer>
  </div>;
}

/** "90s", "5m", "1.5h" as a protobuf Duration; undefined when empty; null when unreadable. */
export function parseTimeout(value: string): Duration | undefined | null {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  const match = /^(\d+(?:\.\d+)?)\s*(ms|s|m|h)$/.exec(trimmed);
  if (!match) return null;
  const milliseconds = Math.round(Number(match[1]) * { ms: 1, s: 1000, m: 60_000, h: 3_600_000 }[match[2] as 'ms' | 's' | 'm' | 'h']);
  return create(DurationSchema, { seconds: BigInt(Math.floor(milliseconds / 1000)), nanos: (milliseconds % 1000) * 1_000_000 });
}

function StartWorkflow({ service, workflow, mode, onStarted }: { service: string; workflow: api.WorkflowDef; mode: 'dark' | 'light'; onStarted: () => void }) {
  const client = useClient(api.WorkflowServiceClient), text = usePlatformText(), action = usePlatformAction<api.StartWorkflowResponse>();
  const [input, setInput] = useState('{}'), [workflowId, setWorkflowId] = useState(''), [timeout, setTimeoutText] = useState(''), [opened, setOpened] = useState<RunRef>();
  const duration = parseTimeout(timeout), blocked = action.disabled || action.pending || duration === null;
  const start = async (payload: string, signal: AbortSignal) => {
    const response = await client.startWorkflow(create(api.StartWorkflowRequestSchema, { service, workflow: workflow.name, input: payload, workflowId: workflowId.trim(), timeout: duration ?? undefined }), { signal });
    onStarted(); return response;
  };
  const exists = action.error instanceof WsStatusError && action.error.code === 6;
  return <div className="grid gap-4">
    <div className="grid gap-3 sm:grid-cols-2">
      <label className="grid gap-1 text-xs text-muted-foreground">{text('workflowIdOptional')}<Input className="h-8 font-mono text-xs" value={workflowId} placeholder={text('workflowIdDefault', { service, workflow: workflow.name })} onChange={(event) => setWorkflowId(event.target.value)} /></label>
      <label className="grid gap-1 text-xs text-muted-foreground">{text('timeoutOptional')}<Input className="h-8 font-mono text-xs" value={timeout} placeholder={text('timeoutPlaceholder')} aria-invalid={duration === null} onChange={(event) => setTimeoutText(event.target.value)} />
        {duration === null && <span className="text-destructive" role="alert">{text('invalidTimeout')}</span>}</label>
    </div>
    {workflow.input ? <SchemaForm schema={workflow.input} label={text('input')} submitLabel={text('startAction')} disabled={blocked} onSubmit={async (baked, signal) => {
      const response = await action.run((abort) => start(nativeJSON(structToNative(baked.values)), AbortSignal.any([signal, abort])));
      if (response === undefined) throw new Error('Start outcome is unavailable');
    }} /> : <>
      <CodeEditor label={text('input')} value={input} onChange={setInput} language="json" mode={mode} height={160} />
      <div><Button disabled={blocked} onClick={() => void action.run((signal) => start(input, signal))}><Play />{text('startAction')}</Button></div>
    </>}
    {action.error !== undefined && <p className="m-0 text-xs text-destructive" role="alert">{text(exists ? 'alreadyRunning' : 'mutationFailed')}</p>}
    {action.value && <div className="flex items-center gap-2 rounded-lg border border-success/30 bg-success/10 px-3 py-2 text-sm" role="status">
      <StatusBadge tone="success">{text('startedRun')}</StatusBadge><span className="min-w-0 flex-1 truncate font-mono text-xs">{action.value.workflowId}</span>
      <Button size="xs" variant="outline" onClick={() => setOpened(action.value)}>{text('openRun')}</Button></div>}
    <RunDrawer run={opened} onClose={() => setOpened(undefined)} mode={mode} />
  </div>;
}

/** Schedules of a service: declaration against Temporal's state, pause/resume with a note, trigger, and links to their runs. */
export function SchedulesPanel(props: { service: string; mode: 'dark' | 'light'; onShowRuns?: (prefix: string) => void; onTriggered?: () => void }) { return <ServiceSchedules key={props.service} {...props} />; }
function ServiceSchedules({ service, mode, onShowRuns, onTriggered }: { service: string; mode: 'dark' | 'light'; onShowRuns?: (prefix: string) => void; onTriggered?: () => void }) {
  const client = useClient(api.ScheduleServiceClient), text = usePlatformText();
  const pages = useRunPages();
  const state = usePlatformQuery(`schedules:${service}:${Array.from(pages.page)}`, (signal) => client.listSchedules(create(api.ListSchedulesRequestSchema, { service, pageSize: 20, pageToken: pages.page }), { signal }));
  const [opened, setOpened] = useState<RunRef>();
  const [editing, setEditing] = useState<string | null>(null);
  const schedules = state.value?.schedules ?? [];
  return <Panel title={<><CalendarClock className="size-4 text-muted-foreground" />{text('schedules')}</>} count={schedules.length} flush footer={<RunPager pages={pages} next={state.value?.nextPageToken} loading={state.loading} />}
    actions={<><Button size="xs" variant="outline" onClick={() => setEditing('')}>{text('createSchedule')}</Button><Button size="icon-sm" variant="ghost" aria-label={text('refresh')} title={text('refresh')} onClick={state.refresh} disabled={state.loading}><RefreshCw className={state.loading ? 'animate-spin' : ''} /></Button></>}>
    {!schedules.length && !state.loading && <EmptyState className="py-6" title={text('noSchedules')} />}
    {schedules.map((schedule) => <ScheduleRow key={schedule.id} onEdit={() => setEditing(schedule.name)} schedule={schedule} onOpenRun={setOpened} onShowRuns={onShowRuns} onChanged={(trigger) => { state.refresh(); if (trigger) onTriggered?.(); }} />)}
    <RunDrawer run={opened} onClose={() => setOpened(undefined)} mode={mode} />
    <DetailDrawer open={editing !== null} onOpenChange={(open) => { if (!open) setEditing(null); }} title={text(editing ? 'editSchedule' : 'createSchedule')}>
      {editing !== null && <ScheduleEditor service={service} name={editing || undefined} mode={mode} onSaved={() => { setEditing(null); state.refresh(); }} />}
    </DetailDrawer>
  </Panel>;
}

function ScheduleRow({ schedule, onOpenRun, onShowRuns, onChanged, onEdit }: { onEdit: () => void; schedule: api.ScheduleInfo; onOpenRun: (run: RunRef) => void; onShowRuns?: (prefix: string) => void; onChanged: (trigger: boolean) => void }) {
  const client = useClient(api.ScheduleServiceClient), text = usePlatformText(), action = usePlatformAction<unknown>();
  const [note, setNote] = useState(''), [expanded, setExpanded] = useState(false);
  const live = schedule.state, paused = live?.paused, spec = schedule.declared?.spec, recent = live?.recentActions.at(-1), next = live?.nextActions[0];
  const drift = live && schedule.declared && schedule.declared.paused !== live.paused;
  const control = (operation: 'pause' | 'resume' | 'trigger' | 'delete') => async (signal: AbortSignal) => {
    const response = await action.run(async (abort) => {
      const options = { signal: AbortSignal.any([signal, abort]) }, request = { service: schedule.service, name: schedule.name };
      if (operation === 'delete') return client.deleteSchedule(create(api.DeleteScheduleRequestSchema, request), options);
      if (operation === 'pause') return client.pauseSchedule(create(api.PauseScheduleRequestSchema, { ...request, note: note.trim() }), options);
      if (operation === 'resume') return client.unpauseSchedule(create(api.UnpauseScheduleRequestSchema, { ...request, note: note.trim() }), options);
      return client.triggerSchedule(create(api.TriggerScheduleRequestSchema, request), options);
    });
    if (response === undefined) throw new Error('Schedule outcome is unavailable');
    setNote(''); onChanged(operation === 'trigger');
  };
  const noteField = <label className="grid gap-1 text-xs text-muted-foreground">{text('note')}<Input className="h-8" value={note} placeholder={text('notePlaceholder')} onChange={(event) => setNote(event.target.value)} /></label>;
  const runLink = (run: RunRef, children: ReactNode) => <button type="button" className="min-w-0 truncate text-left text-link hover:underline" title={run.workflowId} onClick={() => onOpenRun(run)}>{children}</button>;
  const blocked = !live || action.pending || action.disabled, managed = schedule.managedBy !== '';
  return <div role="group" aria-label={schedule.name} className="grid gap-1.5 border-b border-border px-3 py-2.5 last:border-b-0">
    <div className="flex flex-wrap items-center gap-2"><span className="font-mono text-xs">{schedule.name}</span>
      {!live ? <StatusBadge tone="neutral" title={text('notInTemporalHelp')}>{text('notInTemporal')}</StatusBadge> : <StatusBadge tone={paused ? 'warning' : 'success'}>{text(paused ? 'paused' : 'active')}</StatusBadge>}
      {managed && <Badge variant="outline" className="font-mono" title={text('managedHelp', { file: schedule.managedBy })}>{text('managedBy', { file: schedule.managedBy })}</Badge>}
      {drift && <Badge variant="outline" className="text-warning" title={text('driftHelp', { declared: text(schedule.declared!.paused ? 'paused' : 'active'), actual: text(paused ? 'paused' : 'active') })}>{text('drift')}</Badge>}
      <span className="ml-auto text-2xs text-muted-foreground">{text('runsCount', { count: Number(live?.actionCount ?? 0n) })}</span></div>
    <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
      <Badge variant="outline" className="font-mono">{spec?.case === 'cron' ? spec.value : spec?.case === 'every' ? `every ${durationText(spec.value)}` : '—'}</Badge>
      <span>→ {live?.workflowType || schedule.declared?.workflow}</span>
      {schedule.declared && <span>· {enumLabel(ScheduleOverlap, schedule.declared.overlap)}</span>}
    </div>
    {!live && <p className="m-0 text-xs text-muted-foreground">{text('notInTemporalHelp')}</p>}
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
      {next && !paused && <span>{text('nextRun')} <Timestamp value={date(next)} /></span>}
      {recent && runLink({ workflowId: recent.workflowId, runId: recent.runId }, <>{text('lastRun')} <Timestamp value={date(recent.actualTime)} /></>)}
      {live?.note && <span className="italic">{live.note}</span>}
    </div>
    <div className="flex flex-wrap items-center gap-1.5">
      {!managed && <Button size="sm" variant="ghost" disabled={blocked} onClick={onEdit}>{text('editSchedule')}</Button>}
      {!managed && <ConfirmAction trigger={text('deleteSchedule')} title={text('deleteSchedule')} description={text('deleteScheduleHelp')} disabled={blocked} onConfirm={control('delete')} />}
      {paused ? <ConfirmAction trigger={<><Play className="size-3.5" />{text('resume')}</>} title={text('resumeConfirm', { name: schedule.name })} description={text('resumeHelp')} disabled={blocked} onConfirm={control('resume')}>{noteField}</ConfirmAction>
        : <ConfirmAction trigger={<><Pause className="size-3.5" />{text('pause')}</>} title={text('pauseConfirm', { name: schedule.name })} description={text('pauseHelp')} disabled={blocked} onConfirm={control('pause')}>{noteField}</ConfirmAction>}
      <ConfirmAction trigger={<><Zap className="size-3.5" />{text('trigger')}</>} title={text('triggerConfirm', { name: schedule.name })} description={text('triggerHelp')} disabled={blocked} onConfirm={control('trigger')} />
      {onShowRuns && <Button size="sm" variant="ghost" onClick={() => onShowRuns(`${schedule.id}-`)}><History className="size-3.5" />{text('runsOfSchedule')}</Button>}
      {live && <Button size="icon-sm" variant="ghost" className="ml-auto" aria-expanded={expanded} aria-label={text('scheduleDetails', { name: schedule.name })} title={text('scheduleDetails', { name: schedule.name })} onClick={() => setExpanded(!expanded)}>{expanded ? <ChevronDown /> : <ChevronRight />}</Button>}
    </div>
    {action.error !== undefined && <p className="m-0 text-xs text-destructive" role="alert">{text('mutationFailed')}</p>}
    {live && expanded && <KeyValueList className="mt-1 text-xs" items={[
      { label: text('nextActions'), value: live.nextActions.length ? <span className="grid gap-0.5">{live.nextActions.map((time, index) => <Timestamp key={index} value={date(time)} absolute />)}</span> : '—' },
      { label: text('recentActions'), value: live.recentActions.length ? <span className="grid gap-0.5">{[...live.recentActions].reverse().map((entry, index) => <span key={index}>{entry.workflowId ? runLink({ workflowId: entry.workflowId, runId: entry.runId }, <Timestamp value={date(entry.actualTime)} absolute />) : <Timestamp value={date(entry.actualTime)} absolute />}</span>)}</span> : '—' },
      { label: text('runningRuns'), value: live.runningWorkflowIds.length ? <span className="grid gap-0.5">{live.runningWorkflowIds.map((id) => <span key={id}>{runLink({ workflowId: id, runId: '' }, <span className="font-mono">{id}</span>)}</span>)}</span> : '—' },
      { label: text('missedCatchup'), value: live.missedCatchupWindow.toString(), mono: true },
      { label: text('overlapSkipped'), value: live.overlapSkipped.toString(), mono: true },
      { label: text('created'), value: live.created ? <Timestamp value={date(live.created)} absolute /> : '—' },
      { label: text('updated'), value: live.updated ? <Timestamp value={date(live.updated)} absolute /> : '—' },
      { label: text('owner'), value: live.owner || '—', mono: true },
    ]} />}
  </div>;
}
