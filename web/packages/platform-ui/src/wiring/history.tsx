import { useState, type ReactNode } from 'react';
import { create } from '@bufbuild/protobuf';
import * as api from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { Badge, Button, ConfirmAction, Count, DetailDrawer, EmptyState, Panel, StatusBadge, Switch, Timestamp, type StatusTone } from '@backplane-labs/backplane-ui';
import { CodeEditor, DiffViewer, JSONViewer } from '@backplane-labs/backplane-editors';
import { CircleCheck, CircleX, FilePen, FlaskConical, History, Play, RefreshCw, RotateCcw, Sparkles } from 'lucide-react';
import { usePlatformAction, usePlatformQuery } from '../runtime.js';
import { usePlatformText } from '../locales.js';
import { date, durationText, enumLabel } from '../format.js';
import { RunDrawer, RunPager, RunStatusFilter, RunTable, useRunPages, type RunRef } from '../runs.js';
import { toYAML, type Definition, type WiringKind } from './document.js';
import { shortActor } from './actor.js';
import { shapeOf, type Shape } from './shape.js';

type Mode = 'dark' | 'light';

export const stepTone: Record<api.StepRunStatus, StatusTone> = {
  [api.StepRunStatus.UNSPECIFIED]: 'neutral', [api.StepRunStatus.NOT_RUN]: 'neutral', [api.StepRunStatus.SCHEDULED]: 'info', [api.StepRunStatus.STARTED]: 'info',
  [api.StepRunStatus.COMPLETED]: 'success', [api.StepRunStatus.FAILED]: 'danger', [api.StepRunStatus.TIMED_OUT]: 'danger', [api.StepRunStatus.CANCELED]: 'warning',
};

/** What a binding's or rule's history views act on. */
export type Subject = { kind: 'binding'; hook: string } | { kind: 'rule'; id: string };

interface VersionEntry { version: bigint; author: string; comment: string; createdAt?: api.BindingVersion['createdAt']; deleted: boolean; rollbackOf: bigint; name?: string; definition?: Definition }

/** Saved versions, newest first; one opens as a YAML diff against the current version, with rollback. */
export function Versions({ subject, current, mode, onChanged, onOpenDraft }: { subject: Subject; current?: bigint; mode: Mode; onChanged: () => void; onOpenDraft?: (text: string) => void }) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText(), action = usePlatformAction<unknown>();
  const key = subject.kind === 'binding' ? subject.hook : subject.id;
  const state = usePlatformQuery(`wiring-versions:${subject.kind}:${key}:${current}`, async (signal): Promise<VersionEntry[]> => subject.kind === 'binding'
    ? (await bindings.listBindingVersions(create(api.ListBindingVersionsRequestSchema, { hook: subject.hook, pageSize: 200 }), { signal })).versions
    : (await rules.listRuleVersions(create(api.ListRuleVersionsRequestSchema, { id: subject.id, pageSize: 200 }), { signal })).versions);
  const [selected, setSelected] = useState<VersionEntry>();
  const versions = state.value ?? [], latest = versions.find((entry) => entry.version === current);
  const kind: WiringKind = subject.kind;
  const yaml = (entry?: VersionEntry) => entry?.definition ? toYAML(kind, entry.definition) : '';
  const rollback = (entry: VersionEntry) => async (signal: AbortSignal) => {
    const result = await action.run((abort) => { const options = { signal: AbortSignal.any([signal, abort]) };
      return subject.kind === 'binding'
        ? bindings.rollbackBinding(create(api.RollbackBindingRequestSchema, { hook: subject.hook, version: entry.version, baseVersion: current ?? 0n }), options)
        : rules.rollbackRule(create(api.RollbackRuleRequestSchema, { id: subject.id, version: entry.version, baseVersion: current }), options); });
    if (result === undefined) throw new Error('Rollback outcome is unavailable');
    setSelected(undefined); onChanged();
  };
  return <Panel fill flush title={<><History className="size-4 text-muted-foreground" />{text('versions')}</>} count={versions.length}
    actions={<Button size="icon-sm" variant="ghost" aria-label={text('refresh')} title={text('refresh')} onClick={state.refresh} disabled={state.loading}><RefreshCw className={state.loading ? 'animate-spin' : ''} /></Button>}>
    {!versions.length && <EmptyState className="py-8" title={state.loading ? text('loading') : text('noVersions')} />}
    <ol className="m-0 list-none p-0">{versions.map((entry) => <li key={entry.version.toString()}><button type="button" onClick={() => setSelected(entry)} className="flex w-full items-start gap-2.5 border-b border-border px-3 py-2 text-left hover:bg-raised">
      <Count className="mt-0.5">v{entry.version.toString()}</Count>
      <span className="min-w-0 flex-1"><span className="block truncate text-sm">{entry.deleted ? <span className="text-destructive">{text('deleted')}</span> : entry.comment || <span className="text-muted-foreground italic">{text('noComment')}</span>}</span>
        <span className="block truncate text-xs text-muted-foreground">{entry.name && <>{entry.name} · </>}<span title={entry.author}>{shortActor(entry.author)}</span> · <Timestamp value={date(entry.createdAt)} />{entry.rollbackOf > 0n && ` · ${text('rollbackOf', { revision: entry.rollbackOf.toString() })}`}</span></span>
      {entry.version === current && <StatusBadge tone="accent" dot={false}>{text('current')}</StatusBadge>}
    </button></li>)}</ol>
    <DetailDrawer open={!!selected} onOpenChange={(open) => { if (!open) setSelected(undefined); }} size="xl" title={selected && `${selected.name ? `${selected.name} ` : ''}v${selected.version}`} description={selected?.comment}
      actions={selected && selected.version !== current && <>
        <ConfirmAction trigger={<><RotateCcw className="size-3.5" />{text('rollbackToVersion', { version: selected.version.toString() })}</>}
          title={text('rollbackConfirm')} description={text(subject.kind === 'binding' ? 'rollbackBindingHelp' : 'rollbackRuleHelp')} disabled={action.pending || action.disabled} onConfirm={rollback(selected)} />
        {onOpenDraft && selected.definition && <Button size="sm" variant="outline" onClick={() => { onOpenDraft(yaml(selected)); setSelected(undefined); }}><FilePen />{text('openAsDraft')}</Button>}
      </>}>
      {selected && <div className="grid gap-2"><div className="text-xs text-muted-foreground">{text('diffAgainstCurrent')}</div>
        <DiffViewer label={text('versions')} before={yaml(selected)} after={yaml(latest)} language="yaml" mode={mode} height={520} /></div>}
      {action.error !== undefined && <p className="m-0 mt-2 text-xs text-destructive" role="alert">{text('mutationFailed')}</p>}
    </DetailDrawer>
  </Panel>;
}

/** A JSON example of a schema: every field with a value of its type. */
export function exampleOf(shape: Shape, depth = 0): unknown {
  switch (shape.kind) {
    case 'object': return depth > 4 ? {} : Object.fromEntries((shape.fields ?? []).map((field) => [field.name, exampleOf(field.shape, depth + 1)]));
    case 'map': return {};
    case 'list': return depth > 4 ? [] : [exampleOf(shape.elem ?? { kind: 'dyn' }, depth + 1)];
    case 'string': return shape.choices?.[0] ?? '';
    case 'int': case 'uint': case 'double': return 0;
    case 'bool': return false;
    default: return null;
  }
}

/**
 * A test run of the draft (when it differs from the saved version) or the
 * saved version, on a sample input; its run is reported for the graph overlay.
 */
export function TestRun({ subject, draft, dirty, schema, mode, onRun }: {
  subject: Subject | { kind: 'rule'; id: '' }; draft?: Definition; dirty: boolean; schema?: Parameters<typeof shapeOf>[0]; mode: Mode; onRun: (run: RunRef | undefined) => void;
}) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText();
  const action = usePlatformAction<{ violations: api.Violation[]; result?: api.CallResult; matched?: boolean; error?: string }>();
  const [input, setInput] = useState('{}'), [dryRun, setDryRun] = useState(false), [useDraft, setUseDraft] = useState(true);
  const fromDraft = (dirty || (subject.kind === 'rule' && !subject.id)) && useDraft;
  const run = () => void action.run(async (signal) => {
    const response = subject.kind === 'binding'
      ? await bindings.testBinding(create(api.TestBindingRequestSchema, fromDraft ? { definition: draft as api.BindingDefinition, input } : { hook: subject.hook, input }), { signal })
      : await rules.testRule(create(api.TestRuleRequestSchema, fromDraft || !subject.id ? { definition: draft as api.RuleDefinition, event: input, dryRun } : { id: subject.id, event: input, dryRun }), { signal });
    onRun(response.result?.workflowId ? { workflowId: response.result.workflowId, runId: response.result.runId } : undefined);
    return { violations: response.violations, result: response.result, matched: 'matched' in response ? response.matched : undefined, error: 'error' in response ? response.error : undefined };
  });
  return <Panel fill title={<><FlaskConical className="size-4 text-muted-foreground" />{text(subject.kind === 'binding' ? 'testBinding' : 'testRule')}</>} description={text(subject.kind === 'binding' ? 'testBindingHelp' : 'testRuleHelp')}>
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <span className="text-xs text-muted-foreground">{text(subject.kind === 'binding' ? 'input' : 'payload')}</span>
        {schema && <Button size="xs" variant="ghost" onClick={() => setInput(JSON.stringify(exampleOf(shapeOf(schema)), null, 2))}><Sparkles />{text('exampleFromSchema')}</Button>}
        {(dirty || (subject.kind === 'rule' && !subject.id)) && <label className="ml-auto flex items-center gap-2 text-xs text-muted-foreground"><Switch size="sm" checked={useDraft} onCheckedChange={setUseDraft} disabled={subject.kind === 'rule' && !subject.id} />{text('testDraft')}</label>}
      </div>
      <CodeEditor label={text('input')} value={input} onChange={setInput} language="json" mode={mode} height={200} />
      <div className="flex items-center gap-3">
        <Button size="sm" disabled={action.pending || action.disabled || (fromDraft && !draft)} onClick={run}><Play />{action.pending ? text('pending') : text(fromDraft ? 'runDraftTest' : 'runTest')}</Button>
        {subject.kind === 'rule' && <label className="flex items-center gap-2 text-xs text-muted-foreground"><Switch size="sm" checked={dryRun} onCheckedChange={setDryRun} />{text('dryRun')}</label>}
      </div>
      {action.error !== undefined && <p className="m-0 text-xs text-destructive" role="alert">{text('mutationFailed')}</p>}
      {action.value && <div className="grid gap-2">
        {!!action.value.violations.length && <ul className="m-0 grid gap-1 pl-0 text-xs text-destructive" role="alert">{action.value.violations.map((violation, index) => <li key={index} className="list-none"><span className="font-mono">{violation.path}</span> {violation.message}</li>)}</ul>}
        {action.value.matched !== undefined && !action.value.violations.length && <div className="flex items-center gap-2 text-sm">{action.value.matched ? <><CircleCheck className="size-4 text-success" />{text('matched')}</> : <><CircleX className="size-4 text-muted-foreground" />{text('notMatched')}</>}</div>}
        {action.value.error && <pre className="m-0 font-mono text-xs whitespace-pre-wrap text-destructive">{action.value.error}</pre>}
        {action.value.result && <CallOutcome result={action.value.result} subject={subject} mode={mode} />}
      </div>}
    </div>
  </Panel>;
}

function CallOutcome({ result, subject, mode }: { result: api.CallResult; subject: Subject | { kind: 'rule'; id: '' }; mode: Mode }) {
  const text = usePlatformText(), [opened, setOpened] = useState<RunRef>();
  return <div className="grid gap-2">
    <div className="flex flex-wrap items-center gap-2 text-sm">{result.error ? <StatusBadge tone="danger">{text('failed')}</StatusBadge> : <StatusBadge tone="success">{text('succeeded')}</StatusBadge>}
      {result.took && <span className="text-xs text-muted-foreground">{durationText(result.took)}</span>}
      {result.workflowId && <button type="button" className="min-w-0 truncate font-mono text-2xs text-link hover:underline" title={text('openRun')} onClick={() => setOpened({ workflowId: result.workflowId, runId: result.runId })}>{result.workflowId}</button>}</div>
    {result.error ? <pre className="m-0 rounded-md border border-destructive/30 bg-destructive/10 p-2 font-mono text-xs whitespace-pre-wrap text-destructive">{result.errorType && `${result.errorType}: `}{result.error}</pre>
      : <JSONViewer label={text('output')} value={result.output || 'null'} mode={mode} />}
    <SubjectRunDrawer subject={subject} run={opened} onClose={() => setOpened(undefined)} mode={mode} />
  </div>;
}

/** A run of a binding or rule in the run drawer, with its steps. */
export function SubjectRunDrawer({ subject, run, onClose, mode, children }: { subject: Subject | { kind: 'rule'; id: '' }; run?: RunRef; onClose: () => void; mode: Mode; children?: ReactNode }) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient);
  const source = subject.kind === 'binding' ? {
    load: async (ref: RunRef, signal: AbortSignal) => (await bindings.getBindingRun(create(api.GetBindingRunRequestSchema, ref), { signal })).run ?? create(api.GetRunResponseSchema),
    cancel: (ref: RunRef, signal: AbortSignal) => bindings.cancelBindingRun(create(api.CancelBindingRunRequestSchema, ref), { signal }),
  } : {
    load: async (ref: RunRef, signal: AbortSignal) => (await rules.getRuleRun(create(api.GetRuleRunRequestSchema, { id: subject.id, ...ref }), { signal })).run ?? create(api.GetRunResponseSchema),
    cancel: (ref: RunRef, signal: AbortSignal) => rules.cancelRuleRun(create(api.CancelRuleRunRequestSchema, { id: subject.id, ...ref }), { signal }),
  };
  return <RunDrawer run={run} onClose={onClose} mode={mode} source={source}>{run && <><StepTimeline subject={subject} run={run} />{children}</>}</RunDrawer>;
}

/** The steps of one run: its calls from history, for the drawer and the graph overlay. */
export function useRunSteps(subject: Subject | { kind: 'rule'; id: '' }, run?: RunRef, refresh = 0) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient);
  return usePlatformQuery(`wiring-run-steps:${subject.kind}:${run?.workflowId}:${run?.runId}:${refresh}`, async (signal): Promise<api.StepRun[]> => {
    if (!run) return [];
    // An unsaved rule's test run goes by the empty id.
    if (subject.kind === 'rule') return (await rules.getRuleRun(create(api.GetRuleRunRequestSchema, { id: subject.id, ...run }), { signal })).steps;
    return (await bindings.getBindingRun(create(api.GetBindingRunRequestSchema, run), { signal })).steps;
  });
}

export function StepTimeline({ subject, run }: { subject: Subject | { kind: 'rule'; id: '' }; run: RunRef }) {
  const text = usePlatformText(), state = useRunSteps(subject, run);
  if (!state.value?.length) return null;
  return <Panel title={text('steps', { count: state.value.length })} flush>
    {state.value.map((step, index) => { const start = date(step.startedTime), end = date(step.closeTime); return <div key={index} className={`grid gap-0.5 border-b border-border py-2 pr-3 text-xs last:border-b-0 ${step.parent ? 'pl-7' : 'pl-3'}`}>
      <div className="flex flex-wrap items-center gap-2"><StatusBadge tone={stepTone[step.status]}>{enumLabel(api.StepRunStatus, step.status)}</StatusBadge>
        <span className="font-mono font-medium">{step.parent && <span className="text-muted-foreground">{step.parent}/</span>}{step.step}{step.item !== undefined && <span className="text-muted-foreground">[{step.item}]</span>}</span>{step.undo && <Badge variant="outline" className="text-warning">undo</Badge>}<span className="font-mono text-link">{step.activity}</span>
        <span className="ml-auto text-muted-foreground">{step.attempt > 1 && `${text('attemptShort', { attempt: step.attempt })} · `}{start && end ? `${end.getTime() - start.getTime()}ms` : ''}</span></div>
      {step.error && <div className="font-mono text-destructive">{step.errorType && `${step.errorType}: `}{step.error}</div>}
    </div>; })}
  </Panel>;
}

/** Runs of a binding (hook calls) or rule (event runs), or their test runs. */
export function Runs({ subject, mode, onOpen }: { subject: Subject; mode: Mode; onOpen?: (run: RunRef) => void }) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText();
  const [status, setStatus] = useState(api.RunStatus.UNSPECIFIED), [tests, setTests] = useState(false), [selected, setSelected] = useState<RunRef>(), pages = useRunPages();
  const key = subject.kind === 'binding' ? subject.hook : subject.id;
  const state = usePlatformQuery(`wiring-runs:${subject.kind}:${key}:${status}:${tests}:${Array.from(pages.page)}`, async (signal): Promise<{ runs: api.Run[]; nextPageToken: Uint8Array }> => subject.kind === 'binding'
    ? bindings.listBindingRuns(create(api.ListBindingRunsRequestSchema, { hook: subject.hook, status, tests, pageSize: 50, pageToken: pages.page }), { signal })
    : rules.listRuleRuns(create(api.ListRuleRunsRequestSchema, { id: subject.id, status, tests, pageSize: 50, pageToken: pages.page }), { signal }));
  return <Panel fill title={text('runs')} count={state.value?.runs.length} flush
    actions={<><label className="flex items-center gap-2 text-xs text-muted-foreground"><Switch size="sm" checked={tests} onCheckedChange={(value) => { setTests(value); pages.first(); }} />{text('testRuns')}</label>
      <Button size="icon-sm" variant="ghost" aria-label={text('refresh')} title={text('refresh')} onClick={state.refresh} disabled={state.loading}><RefreshCw className={state.loading ? 'animate-spin' : ''} /></Button></>}
    footer={<RunPager pages={pages} next={state.value?.nextPageToken} loading={state.loading} />}>
    <div className="border-b border-border px-3 py-2"><RunStatusFilter value={status} onChange={(value) => { setStatus(value); pages.first(); }} /></div>
    <RunTable runs={state.value?.runs ?? []} loading={state.loading} onOpen={(run) => setSelected({ workflowId: run.workflowId, runId: run.runId })} />
    <SubjectRunDrawer subject={subject} run={selected} onClose={() => setSelected(undefined)} mode={mode}>
      {selected && onOpen && <Button size="sm" variant="outline" className="mt-3" onClick={() => { onOpen(selected); setSelected(undefined); }}>{text('showOnGraph')}</Button>}
    </SubjectRunDrawer>
  </Panel>;
}
