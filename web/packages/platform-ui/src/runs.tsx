import { useEffect, useState, type ReactNode } from 'react';
import { create } from '@bufbuild/protobuf';
import * as api from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { Badge, Button, ConfirmAction, DetailDrawer, Duration, EmptyState, Input, KeyValueList, Panel, Skeleton, StatusBadge, Timestamp, type StatusTone } from '@backplane-labs/backplane-ui';
import { CodeEditor, JSONViewer } from '@backplane-labs/backplane-editors';
import { ArrowLeft, ChevronDown, ChevronRight, CircleX, OctagonX, RefreshCw, Send } from 'lucide-react';
import { usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { date, enumLabel } from './format.js';

export const runTone: Record<api.RunStatus, StatusTone> = {
  [api.RunStatus.UNSPECIFIED]: 'neutral', [api.RunStatus.RUNNING]: 'info', [api.RunStatus.COMPLETED]: 'success', [api.RunStatus.FAILED]: 'danger',
  [api.RunStatus.CANCELED]: 'neutral', [api.RunStatus.TERMINATED]: 'warning', [api.RunStatus.CONTINUED_AS_NEW]: 'neutral', [api.RunStatus.TIMED_OUT]: 'danger',
};
export function RunStatusBadge({ status }: { status: api.RunStatus }) {
  return <StatusBadge tone={runTone[status]}>{enumLabel(api.RunStatus, status)}</StatusBadge>;
}
export const runStatuses = [api.RunStatus.RUNNING, api.RunStatus.COMPLETED, api.RunStatus.FAILED, api.RunStatus.CANCELED, api.RunStatus.TERMINATED, api.RunStatus.TIMED_OUT, api.RunStatus.CONTINUED_AS_NEW];

/** Status filter chips shared by every run list. */
export function RunStatusFilter({ value, onChange }: { value: api.RunStatus; onChange: (status: api.RunStatus) => void }) {
  const text = usePlatformText();
  return <div className="flex flex-wrap gap-1" role="group" aria-label={text('status')}>
    {[api.RunStatus.UNSPECIFIED, ...runStatuses].map((status) => <button key={status} type="button" aria-pressed={value === status} onClick={() => onChange(status)}
      className="h-6 rounded-sm border border-border px-2 text-xs text-muted-foreground hover:text-foreground aria-pressed:border-primary/50 aria-pressed:bg-primary/10 aria-pressed:text-foreground">
      {status === api.RunStatus.UNSPECIFIED ? text('allStatuses') : enumLabel(api.RunStatus, status)}
    </button>)}
  </div>;
}

/** The first 8 characters of a run id's own part: a direct run's id is its workflow id, a path ending in a uuid. */
export function shortRunId(runId: string): string { return (runId.split('/').pop() ?? runId).slice(0, 8); }

/** Dense run table; a row opens the run. */
export function RunTable({ runs, onOpen, loading, empty }: { runs: api.Run[]; onOpen: (run: api.Run) => void; loading?: boolean; empty?: ReactNode }) {
  const text = usePlatformText();
  if (loading && !runs.length) return <div className="grid gap-2 p-3">{[0, 1, 2].map((key) => <Skeleton key={key} className="h-8" />)}</div>;
  if (!runs.length) return <>{empty ?? <EmptyState className="py-8" title={text('noRuns')} />}</>;
  return <table aria-label={text('runs')} className="w-full text-sm">
    <thead className="sticky top-0 z-[1]"><tr className="border-b border-border bg-raised text-left text-xs text-muted-foreground">
      {[text('status'), text('workflow'), text('runId'), text('startedAt'), text('duration'), text('history')].map((label) => <th key={label} className="h-8 px-3 font-medium whitespace-nowrap">{label}</th>)}
    </tr></thead>
    <tbody>{runs.map((run) => <tr key={`${run.workflowId}/${run.runId}`} tabIndex={0} className="cursor-pointer border-b border-border last:border-b-0 hover:bg-raised focus-visible:bg-raised focus-visible:outline-none"
      onClick={() => onOpen(run)} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); onOpen(run); } }}>
      <td className="h-10 px-3"><RunStatusBadge status={run.status} /></td>
      <td className="max-w-80 px-3"><div className="truncate font-mono text-xs" title={run.workflowId}>{run.workflowId}</div><div className="truncate text-2xs text-muted-foreground">{run.workflowType}</div></td>
      <td className="px-3 font-mono text-2xs text-muted-foreground" title={run.runId}>{shortRunId(run.runId)}</td>
      <td className="px-3 text-xs text-muted-foreground"><Timestamp value={date(run.startTime)} /></td>
      <td className="px-3 text-xs">{run.startTime ? run.closeTime ? <Duration milliseconds={date(run.closeTime)!.getTime() - date(run.startTime)!.getTime()} /> : <Duration since={date(run.startTime)} className="text-info" /> : '—'}</td>
      <td className="px-3 font-mono text-xs text-muted-foreground">{run.historyLength.toString()}</td>
    </tr>)}</tbody>
  </table>;
}

/** Page tokens of a run list: the first page, then each older one. */
export function useRunPages() {
  const [pages, setPages] = useState<Uint8Array[]>([new Uint8Array()]);
  return { page: pages.at(-1)!, number: pages.length, first: () => setPages([new Uint8Array()]),
    newer: () => setPages((old) => old.length > 1 ? old.slice(0, -1) : old), older: (token: Uint8Array) => setPages((old) => [...old, new Uint8Array(token)]) };
}
/** Panel footer of a paged run list. */
export function RunPager({ pages, next, loading }: { pages: ReturnType<typeof useRunPages>; next?: Uint8Array; loading: boolean }) {
  const text = usePlatformText();
  return <><span>{text('pageNumber', { page: pages.number })}</span><span className="ml-auto flex gap-1">
    <Button size="xs" variant="ghost" disabled={pages.number < 2} onClick={pages.newer}>{text('newerPage')}</Button>
    <Button size="xs" variant="ghost" disabled={!next?.length || loading} onClick={() => { if (next) pages.older(next); }}>{text('olderPage')}</Button></span></>;
}

/** A run by workflow id and run id (empty run id: the latest). */
export interface RunRef { workflowId: string; runId: string }
/** Hooks for runs owned by another service API (binding and rule runs). */
export interface RunSource { load?: (run: RunRef, signal: AbortSignal) => Promise<api.GetRunResponse>; cancel?: (run: RunRef, signal: AbortSignal) => Promise<unknown> }
/** How often an open running run is refreshed. */
const runPoll = 3000;
const refOf = (run: RunRef) => `${run.workflowId}/${run.runId}`;

/** Run detail in a drawer: summary, memo, input/result, failure, pending activities, history, and run controls. Links to the parent, continued or child run open that run in the same drawer (through WorkflowService). */
export function RunDrawer({ run, onClose, mode, children, source }: { run?: RunRef; onClose: () => void; mode: 'dark' | 'light'; children?: ReactNode; source?: RunSource }) {
  const text = usePlatformText(), [linked, setLinked] = useState<{ from: string; run: RunRef }>();
  const followed = run && linked?.from === refOf(run) ? linked.run : undefined, shown = followed ?? run;
  return <DetailDrawer open={!!run} onOpenChange={(open) => { if (!open) { setLinked(undefined); onClose(); } }} size="lg" title={shown?.workflowId} description={shown && <span className="font-mono">{text('runId')} {shown.runId || '—'}</span>}
    actions={run && followed && <Button size="xs" variant="outline" onClick={() => setLinked(undefined)}><ArrowLeft />{run.workflowId}</Button>}>
    {run && shown && <RunDetail key={refOf(shown)} workflowId={shown.workflowId} runId={shown.runId} mode={mode} source={followed ? undefined : source} onOpenRun={(next) => setLinked({ from: refOf(run), run: next })}>{followed ? undefined : children}</RunDetail>}
  </DetailDrawer>;
}

/** Workflow id (and run) as a link that opens it. */
function RunLink({ run, onOpen, label }: { run: RunRef; onOpen: (run: RunRef) => void; label?: string }) {
  const text = usePlatformText();
  return <button type="button" className="min-w-0 truncate text-left font-mono text-xs text-link hover:underline" title={text('relatedRun', { id: refOf(run) })} onClick={() => onOpen(run)}>{label ?? run.workflowId}</button>;
}

/** Memo value (JSON text) for display: strings unquoted. */
function memoText(value: string): string {
  if (!value.startsWith('"')) return value;
  try { const parsed: unknown = JSON.parse(value); return typeof parsed === 'string' ? parsed : value; } catch { return value; }
}

function RunDetail({ workflowId, runId, mode, children, source, onOpenRun }: { workflowId: string; runId: string; mode: 'dark' | 'light'; children?: ReactNode; source?: RunSource; onOpenRun: (run: RunRef) => void }) {
  const client = useClient(api.WorkflowServiceClient), text = usePlatformText(), action = usePlatformAction<unknown>();
  const state = usePlatformQuery(`run:${workflowId}:${runId}`, (signal) => source?.load ? source.load({ workflowId, runId }, signal) : client.getRun(create(api.GetRunRequestSchema, { workflowId, runId }), { signal }));
  const [reason, setReason] = useState(''), [signalName, setSignalName] = useState(''), [signalInput, setSignalInput] = useState(''), [open, setOpen] = useState<Set<bigint>>(new Set());
  const detail = state.value, run = detail?.run, running = run?.status === api.RunStatus.RUNNING, refresh = state.refresh;
  // A running run is refreshed while the drawer shows it; closing unmounts and stops it.
  useEffect(() => { if (!running) return; const timer = setInterval(refresh, runPoll); return () => clearInterval(timer); }, [running, refresh]);
  if (!detail) return state.error !== undefined ? <EmptyState icon={<CircleX />} title={text('error')} action={<Button size="sm" variant="outline" onClick={state.refresh}>{text('retry')}</Button>} /> : <div className="grid gap-2"><Skeleton className="h-24" /><Skeleton className="h-48" /></div>;
  const control = (execute: (signal: AbortSignal) => Promise<unknown>) => async (signal: AbortSignal) => {
    const result = await action.run((abort) => execute(AbortSignal.any([signal, abort])));
    if (result === undefined) throw new Error('Run control outcome is unavailable'); state.refresh();
  };
  const toggle = (id: bigint) => setOpen((old) => { const next = new Set(old); if (next.has(id)) next.delete(id); else next.add(id); return next; });
  const memo = Object.entries(run?.memo ?? {}).sort(([a], [b]) => a.localeCompare(b));
  return <div className="grid gap-4">
    {running && <p className="m-0 inline-flex items-center gap-1.5 text-2xs text-muted-foreground" role="status"><RefreshCw className={`size-3 ${state.loading ? 'animate-spin' : ''}`} />{text('autoRefresh')}</p>}
    {run && <KeyValueList items={[
      { label: text('status'), value: <RunStatusBadge status={run.status} /> },
      { label: text('workflow'), value: run.workflowType, mono: true }, { label: text('taskQueue'), value: run.taskQueue, mono: true },
      { label: text('startedAt'), value: <Timestamp value={date(run.startTime)} absolute /> },
      { label: text('closedAt'), value: run.closeTime ? <Timestamp value={date(run.closeTime)} absolute /> : <span className="text-info">{text('stillRunning')}</span> },
      { label: text('history'), value: run.historyLength.toString(), mono: true },
      ...(run.memo.source ? [{ label: text('startedBy'), value: memoText(run.memo.source), mono: true }] : []),
      ...(run.parentWorkflowId ? [{ label: text('parentRun'), value: <RunLink run={{ workflowId: run.parentWorkflowId, runId: run.parentRunId }} onOpen={onOpenRun} /> }] : []),
      ...(detail.continuedRunId ? [{ label: text('continuedAs'), value: <RunLink run={{ workflowId: run.workflowId, runId: detail.continuedRunId }} label={detail.continuedRunId} onOpen={onOpenRun} /> }] : []),
    ]} />}
    {memo.filter(([key]) => key !== 'source').length > 0 && <div><div className="mb-1.5 text-xs text-muted-foreground">{text('memo')}</div>
      <KeyValueList items={memo.filter(([key]) => key !== 'source').map(([key, value]) => ({ key, label: <span className="font-mono text-xs">{key}</span>, value: memoText(value), mono: true }))} /></div>}
    {detail.failure && <div className="rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive" role="alert"><div className="font-medium">{detail.failureType || text('failure')}</div><pre className="m-0 mt-1 font-mono text-xs whitespace-pre-wrap">{detail.failure}</pre></div>}
    {children}
    <div className="grid gap-3 lg:grid-cols-2">
      <div className="min-w-0"><div className="mb-1.5 text-xs text-muted-foreground">{text('input')}</div><JSONViewer label={text('input')} value={detail.input || 'null'} mode={mode} /></div>
      <div className="min-w-0"><div className="mb-1.5 text-xs text-muted-foreground">{text('output')}</div><JSONViewer label={text('output')} value={detail.result || 'null'} mode={mode} /></div>
    </div>
    {detail.pendingActivities.length > 0 && <Panel title={text('pendingActivities')} count={detail.pendingActivities.length} flush>
      {detail.pendingActivities.map((activity) => <div key={activity.activityId} className="grid gap-1.5 border-b border-border px-3 py-2 text-xs last:border-b-0">
        <div className="flex items-center gap-2"><span className="font-mono">{activity.activityType}</span><Badge variant="outline">{activity.state.toLowerCase()}</Badge><span className="ml-auto text-muted-foreground">{text('attempt', { attempt: activity.attempt, max: activity.maximumAttempts || '∞' })}</span></div>
        <KeyValueList className="text-xs" items={[
          { label: text('activityId'), value: activity.activityId, mono: true },
          { label: text('lastStarted'), value: activity.lastStartedTime ? <Timestamp value={date(activity.lastStartedTime)} absolute /> : '—' },
          { label: text('lastHeartbeat'), value: activity.lastHeartbeatTime ? <Timestamp value={date(activity.lastHeartbeatTime)} absolute /> : '—' },
          { label: text('nextAttempt'), value: activity.nextAttemptTime ? <Timestamp value={date(activity.nextAttemptTime)} absolute /> : '—' },
          { label: text('lastWorker'), value: activity.lastWorker || '—', mono: true },
          ...(activity.lastFailure ? [{ label: text('lastFailure'), value: <span className="text-destructive">{activity.lastFailure}</span> }] : []),
        ]} />
      </div>)}
    </Panel>}
    <Panel title={text('history')} count={detail.history.length} flush footer={detail.truncated ? text('partial') : undefined}>
      <ol className="m-0 max-h-[32rem] list-none overflow-y-auto p-0">{detail.history.map((event) => { const expanded = open.has(event.id), related = event.workflowId && (event.workflowId !== workflowId || event.runId !== run?.runId);
        return <li key={event.id.toString()} className="grid grid-cols-[20px_40px_minmax(0,1fr)_auto] items-start gap-x-2 border-b border-border px-3 py-1.5 text-xs last:border-b-0">
          {event.payload ? <button type="button" className="grid size-5 place-items-center rounded-sm text-muted-foreground hover:bg-raised hover:text-foreground" aria-expanded={expanded} aria-label={text(expanded ? 'hidePayload' : 'showPayload', { id: event.id.toString() })} onClick={() => toggle(event.id)}>{expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}</button> : <span />}
          <span className="font-mono leading-5 text-muted-foreground">{event.id.toString()}</span>
          <span className="min-w-0 leading-5"><span className={`font-medium ${event.failure ? 'text-destructive' : ''}`}>{event.type}</span>{event.summary && <span className="ml-2 text-muted-foreground">{event.summary}</span>}
            {related && <span className="ml-2 inline-flex max-w-full"><RunLink run={{ workflowId: event.workflowId, runId: event.runId }} label={event.runId ? `${event.workflowId} · ${event.runId.slice(0, 8)}` : event.workflowId} onOpen={onOpenRun} /></span>}
            {event.failure && <span className="block text-destructive">{event.failure}</span>}</span>
          <span className="leading-5 text-muted-foreground"><Timestamp value={date(event.time)} /></span>
          {expanded && <div className="col-span-4 mt-1.5 min-w-0"><JSONViewer label={text('eventPayload', { id: event.id.toString() })} value={event.payload} mode={mode} height={160} /></div>}
        </li>; })}</ol>
    </Panel>
    {running && <Panel title={text('controls')} description={source ? text('controlsViaWorkflows') : undefined}>
      <div className="grid gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <ConfirmAction trigger={<><CircleX className="size-3.5" />{text('cancel')}</>} title={text('cancelConfirm')} description={text('cancelHelp')} disabled={action.pending || action.disabled}
            onConfirm={control((signal) => source?.cancel ? source.cancel({ workflowId, runId }, signal) : client.cancelRun(create(api.CancelRunRequestSchema, { workflowId, runId }), { signal }))} />
          <Input className="h-8 w-56" aria-label={text('reason')} placeholder={text('terminateReason')} value={reason} onChange={(event) => setReason(event.target.value)} />
          <ConfirmAction trigger={<><OctagonX className="size-3.5" />{text('terminate')}</>} title={text('terminateConfirm')} description={text('terminateHelp')} disabled={action.pending || action.disabled}
            onConfirm={control((signal) => client.terminateRun(create(api.TerminateRunRequestSchema, { workflowId, runId, reason }), { signal }))} />
        </div>
        <div className="grid gap-2">
          <Input className="h-8" aria-label={text('signalName')} placeholder={text('signalName')} value={signalName} onChange={(event) => setSignalName(event.target.value)} />
          <CodeEditor label={text('signalInput')} value={signalInput} onChange={setSignalInput} language="json" mode={mode} height={80} />
          <p className="m-0 text-2xs text-muted-foreground">{text('signalNoInput')}</p>
          <div><ConfirmAction trigger={<><Send className="size-3.5" />{text('signalRun')}</>} title={text('signalConfirm')} description={signalName} disabled={!signalName || action.pending || action.disabled}
            onConfirm={control((signal) => client.signalRun(create(api.SignalRunRequestSchema, { workflowId, runId, signal: signalName, input: signalInput.trim() }), { signal }))} /></div>
        </div>
        {action.error !== undefined && <p className="m-0 text-xs text-destructive" role="alert">{text('mutationFailed')}</p>}
      </div>
    </Panel>}
  </div>;
}
