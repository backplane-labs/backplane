import { ExecutionGate, useExecutionEnabled } from '../execution.js';
import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { create } from '@bufbuild/protobuf';
import * as api from '@backplane-labs/backplane-api';
import { WsStatusError } from '@backplane-labs/backplane-client';
import { useClient } from '@backplane-labs/backplane-react';
import { Badge, Button, Combobox, ConfirmAction, DetailDrawer, EmptyState, EntityHeader, Input, MetaItem, MetaList, Panel, Skeleton, StatusBadge, TabBar, Timestamp, type StatusTone } from '@backplane-labs/backplane-ui';
import { CodeEditor, DiffViewer, type EditorDiagnostic } from '@backplane-labs/backplane-editors';
import { AlertTriangle, Cable, CircleAlert, CircleCheck, FileCode, FileWarning, LayoutGrid, Map as MapIcon, GitFork, History, Info, Pause, Play, Redo2, Save, Trash2, Undo2, User, Wand2, Zap } from 'lucide-react';
import { usePlatformAction, usePlatformQuery } from '../runtime.js';
import { usePlatformText } from '../locales.js';
import { date, enumLabel } from '../format.js';
import type { RunRef } from '../runs.js';
import { completeYAML } from './complete.js';
import { addStep, edit, editAll, nodeAt, parseDraft, pathAt, templateYAML, toYAML, unescape, type Definition, type Draft, type DraftProblem, type WiringKind } from './document.js';
import { lineColumn, problemsOf, sameDefinition, useAnalysis, useDraftText, useParsedDraft } from './draft.js';
import { Runs, TestRun, Versions, useRunSteps, type Subject } from './history.js';
import { WiringGraph } from './graph.js';
import { shortActor } from './actor.js';
import { StepInspector } from './inspector.js';
import { shapeAt, shapeLabel, shapeOf } from './shape.js';
import { framesOf, type WiringIndex } from './catalog.js';

type Mode = 'dark' | 'light';

/** What the editor opens: a hook's binding, a rule, or a new rule on an event. */
export type WiringTarget = { kind: 'binding'; hook: string } | { kind: 'rule'; id: string; event?: string };
export type WiringView = 'graph' | 'yaml' | 'versions' | 'test' | 'runs';
export const targetKey = (target: WiringTarget) => target.kind === 'binding' ? `binding:${target.hook}` : `rule:${target.id || `new:${target.event ?? ''}`}`;

/** Unsaved drafts by target key: they survive switching between items. */
export interface DraftStore {
  get: (key: string) => StoredDraft | undefined;
  set: (key: string, draft: StoredDraft) => void;
  delete: (key: string) => void;
}
export interface StoredDraft { text: string; base: bigint; name: string }

export const bindingTone: Record<api.BindingState, StatusTone> = {
  [api.BindingState.UNSPECIFIED]: 'neutral', [api.BindingState.BOUND]: 'success', [api.BindingState.UNBOUND]: 'neutral',
  [api.BindingState.REQUIRED_UNBOUND]: 'danger', [api.BindingState.BROKEN]: 'danger',
};
export const ruleTone: Record<api.RuleState, StatusTone> = {
  [api.RuleState.UNSPECIFIED]: 'neutral', [api.RuleState.ACTIVE]: 'success', [api.RuleState.PAUSED]: 'warning',
  [api.RuleState.DELETED]: 'neutral', [api.RuleState.BROKEN]: 'danger',
};

interface Loaded { version?: api.BindingVersion | api.RuleVersion; rule?: api.Rule; violations: api.Violation[] }

export interface WiringEditorProps {
  target: WiringTarget; index: WiringIndex; mode: Mode; view: WiringView; onView: (view: WiringView) => void; drafts: DraftStore;
  /** The binding as the live list shows it (state, required, description). */
  hookInfo?: api.HookBinding;
  /** The version the live list knows as current: a newer one than loaded means someone saved meanwhile. */
  latestVersion?: bigint;
  onSaved: (target: WiringTarget) => void; onDeleted: () => void;
}

/** One binding or rule: its draft in YAML or as a graph, problems, versions, tests and runs. */
export function WiringEditor(props: WiringEditorProps) {
  const { target } = props;
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText();
  const [epoch, setEpoch] = useState(0), key = targetKey(target);
  const loaded = usePlatformQuery(`wiring-item:${key}:${epoch}`, async (signal): Promise<Loaded> => {
    if (target.kind === 'binding') {
      try {
        const response = await bindings.getBinding(create(api.GetBindingRequestSchema, { hook: target.hook }), { signal });
        return { version: response.version, violations: response.violations };
      } catch (error) { if (error instanceof WsStatusError && error.code === 5) return { violations: [] }; throw error; }
    }
    if (!target.id) return { violations: [] };
    const response = await rules.getRule(create(api.GetRuleRequestSchema, { id: target.id }), { signal });
    return { version: response.version, rule: response.rule, violations: response.violations };
  });
  if (!loaded.value) return <Panel fill>{loaded.error !== undefined ? <EmptyState title={text('error')} action={<Button size="sm" variant="outline" onClick={loaded.refresh}>{text('retry')}</Button>} /> : <div className="grid gap-3"><Skeleton className="h-10" /><Skeleton className="h-96" /></div>}</Panel>;
  const version = loaded.value.version;
  return <DraftEditor key={`${key}:${version?.version ?? 0n}:${epoch}`} {...props} loaded={loaded.value} reload={() => setEpoch((value) => value + 1)} />;
}

function DraftEditor({ target, index, mode, view, onView, drafts, hookInfo, latestVersion, onSaved, onDeleted, loaded, reload }: WiringEditorProps & { loaded: Loaded; reload: () => void }) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText();
  const kind: WiringKind = target.kind, key = targetKey(target);
  const executionEnabled = useExecutionEnabled(kind);
  const version = loaded.version, saved = version && !version.deleted ? version.definition : undefined;
  const source = target.kind === 'binding' ? target.hook : (saved as api.RuleDefinition | undefined)?.event ?? target.event ?? '';
  const baseline = useMemo(() => saved ? toYAML(kind, saved) : templateYAML(kind, source), [kind, saved, source]);
  const savedName = (version as api.RuleVersion | undefined)?.name ?? '';
  const loadedVersion = version?.version ?? 0n;
  // Defined in a wiring file: the console reads it, tests drafts of it, never saves it.
  const managedBy = version?.managedBy ?? '';
  const stored = drafts.get(key);
  const draft = useDraftText(stored?.text ?? baseline);
  const [name, setName] = useState(stored?.name ?? savedName), [base, setBase] = useState(stored?.base ?? loadedVersion);
  const [comment, setComment] = useState(''), [reveal, setReveal] = useState<{ from: number; to: number; key: number }>();
  const [minimap, setMinimap] = useState(false);
  const [cursor, setCursor] = useState(0), [selected, setSelected] = useState<string>(), [run, setRun] = useState<RunRef>(), [conflictOpen, setConflictOpen] = useState(false);
  const parsed = useParsedDraft(kind, draft.text);
  const lastGood = useRef<Definition | undefined>(parsed.definition ?? saved);
  if (parsed.definition) lastGood.current = parsed.definition;
  const definition = parsed.definition ?? lastGood.current;
  const analysis = useAnalysis(kind, parsed.definition);
  const problems = useMemo(() => problemsOf(parsed, analysis.current ? analysis.analysis?.violations : undefined), [parsed, analysis.current, analysis.analysis]);
  const errors = problems.filter((problem) => problem.severity === 'error');
  const changed = !sameDefinition(kind, parsed.definition, saved) || (kind === 'rule' && name.trim() !== savedName);
  const textChanged = draft.text !== baseline || name !== savedName;
  const stale = base !== loadedVersion || (latestVersion !== undefined && latestVersion > loadedVersion);
  const runSteps = useRunSteps(target.kind === 'binding' ? { kind: 'binding', hook: target.hook } : { kind: 'rule', id: target.id }, run);
  const subject: Subject | undefined = target.kind === 'binding' ? { kind: 'binding', hook: target.hook } : target.id ? { kind: 'rule', id: target.id } : undefined;

  // Keep the draft in the store while it differs from what is saved, so switching items keeps it.
  useEffect(() => { if (textChanged || base !== loadedVersion) drafts.set(key, { text: draft.text, base, name }); else drafts.delete(key); }, [draft.text, name, base]); // eslint-disable-line react-hooks/exhaustive-deps -- the store is stable per workspace.

  const action = usePlatformAction<{ violations: api.Violation[]; target?: WiringTarget }>();
  const conflict = action.error instanceof WsStatusError && action.error.code === 10;
  const save = () => void action.run(async (signal) => {
    // Save reads the text as it is now, never a parse the render has not caught up with.
    const fresh = parseDraft(kind, draft.text).definition; if (!fresh) return { violations: [] };
    let response: { violations: api.Violation[]; version?: { version: bigint; ruleId?: string } };
    if (kind === 'binding') response = await bindings.saveBinding(create(api.SaveBindingRequestSchema, { definition: fresh as api.BindingDefinition, comment, baseVersion: base }), { signal });
    else response = await rules.saveRule(create(api.SaveRuleRequestSchema, { id: target.kind === 'rule' ? target.id : '', name: name.trim(), definition: fresh as api.RuleDefinition, comment, ...(target.kind === 'rule' && target.id ? { baseVersion: base } : {}) }), { signal });
    if (response.violations.length || !response.version) return { violations: response.violations };
    drafts.delete(key); setComment('');
    const next: WiringTarget = kind === 'binding' ? target : { kind: 'rule', id: response.version.ruleId ?? (target as { id: string }).id };
    onSaved(next); reload();
    return { violations: [], target: next };
  });
  const discard = () => { draft.apply(baseline); setName(savedName); setBase(loadedVersion); drafts.delete(key); };
  const saveProblems: DraftProblem[] = useMemo(() => action.value?.violations.length ? problemsOf(parsed, action.value.violations).filter((problem) => problem.source === 'server') : [], [action.value, parsed]);
  const shown = analysis.current ? problems : [...problems, ...saveProblems];
  const diagnostics: EditorDiagnostic[] = shown.map((problem) => ({ from: problem.from, to: problem.to, severity: problem.severity, message: problem.code ? `${problem.message} (${problem.code})` : problem.message }));
  const goTo = (problem: DraftProblem) => {
    const step = /^\/steps\/([^/]+)/.exec(problem.path ?? '')?.[1];
    if (view === 'graph' && step) { setSelected(step); return; }
    onView('yaml'); setReveal({ from: problem.from, to: problem.to, key: Date.now() });
  };
  const onKey = (event: KeyboardEvent) => {
    const mod = event.metaKey || event.ctrlKey;
    if (mod && event.key === 's') { event.preventDefault(); if (canSave) save(); }
    // In YAML CodeMirror keeps its own history; elsewhere the draft's history applies.
    if (mod && !(event.target as HTMLElement).closest('.cm-editor') && (event.key === 'z' || event.key === 'y')) {
      event.preventDefault(); if (event.key === 'y' || event.shiftKey) draft.redo(); else draft.undo();
    }
  };
  const canSave = !managedBy && changed && !errors.length && !action.pending && !action.disabled && !!parsed.definition && (kind === 'binding' || !!name.trim());
  // Why Save is off, when the reason is the author's to fix.
  const blockedBy = managedBy ? 'managedToSave' as const : !parsed.definition || errors.length ? 'fixProblemsToSave' as const : kind === 'rule' && !name.trim() ? 'nameToSave' as const : undefined;
  const state = kind === 'binding' ? hookInfo?.state : loaded.rule?.state;
  const broken = loaded.violations.length > 0 && !textChanged;
  const title = kind === 'binding' ? source : savedName || text('newRule');
  const meta = <MetaList>
    <MetaItem icon={kind === 'binding' ? <Cable /> : <Zap />} mono>{kind === 'binding' ? text('bindingOf', { hook: source }) : source || text('noEvent')}</MetaItem>
    {version && <MetaItem icon={<History />} mono>v{version.version.toString()}</MetaItem>}
    {version && <MetaItem icon={<User />} mono title={version.author}>{shortActor(version.author)}</MetaItem>}
    {version && <MetaItem><Timestamp value={date(version.createdAt)} /></MetaItem>}
    {managedBy && <MetaItem icon={<FileCode />} mono title={managedBy}>{managedBy}</MetaItem>}
    {textChanged && <Badge variant="outline" className="h-4 border-warning/40 px-1 text-2xs text-warning">{text('draft')}</Badge>}
  </MetaList>;

  return <div className="flex h-full min-h-0 flex-col" onKeyDown={onKey}>
    <EntityHeader level={2} className="mb-3" icon={kind === 'binding' ? <Cable /> : <Zap />} title={<span className={kind === 'binding' ? 'font-mono' : undefined}>{title}</span>} meta={meta}
      status={state !== undefined && (kind === 'binding'
        ? <StatusBadge tone={bindingTone[state as api.BindingState]}>{enumLabel(api.BindingState, state)}</StatusBadge>
        : <StatusBadge tone={ruleTone[state as api.RuleState]}>{enumLabel(api.RuleState, state)}</StatusBadge>)}
      actions={<HeaderActions target={target} loaded={loaded} saved={!!saved} managed={!!managedBy} loadedVersion={loadedVersion} onChanged={reload} onDeleted={onDeleted} />}
      tabs={<TabBar aria-label={title}>{(['graph', 'yaml', 'versions', 'test', 'runs'] as const).filter((entry) => subject || entry === 'graph' || entry === 'yaml' || entry === 'test').map((entry) =>
        <button key={entry} type="button" disabled={!executionEnabled && (entry === 'test' || entry === 'runs')} title={!executionEnabled && (entry === 'test' || entry === 'runs') ? text('executionDisabled') : undefined} aria-current={view === entry ? 'page' : undefined} onClick={() => onView(entry)}
          className="relative -mb-px inline-flex h-9 items-center gap-1.5 border-b-2 border-transparent px-2.5 text-sm text-muted-foreground hover:text-foreground aria-[current=page]:border-primary aria-[current=page]:font-medium aria-[current=page]:text-foreground">
          {text(`wiringView_${entry}`)}{entry === 'yaml' && errors.length > 0 && <span className="size-1.5 rounded-full bg-destructive" />}</button>)}</TabBar>} />
    {managedBy && <Banner tone="info" icon={<FileCode />} title={text('managedTitle', { file: managedBy })}>{text(kind === 'binding' ? 'managedBindingHelp' : 'managedRuleHelp')}</Banner>}
    {broken && <Banner tone="danger" icon={<FileWarning />} title={text('brokenTitle')}>{text('brokenHelp')}</Banner>}
    {stale && !managedBy && <Banner tone="warning" icon={<GitFork />} title={text('staleTitle', { base: base.toString(), current: (latestVersion ?? loadedVersion).toString() })}
      actions={<>{latestVersion !== undefined && latestVersion > loadedVersion && <Button size="xs" variant="outline" onClick={reload}>{text('loadLatest')}</Button>}
        <Button size="xs" variant="outline" onClick={() => setConflictOpen(true)}>{text('compareLatest')}</Button>
        {base !== loadedVersion && <Button size="xs" onClick={() => setBase(loadedVersion)}>{text('continueFrom', { version: loadedVersion.toString() })}</Button>}
        <Button size="xs" variant="ghost" onClick={discard}>{text('discardDraft')}</Button></>}>{text('staleHelp')}</Banner>}
    {conflict && !stale && <Banner tone="warning" icon={<GitFork />} title={text('conflictTitle')} actions={<Button size="xs" variant="outline" onClick={reload}>{text('loadLatest')}</Button>}>{text('conflictHelp')}</Banner>}
    {(view === 'graph' || view === 'yaml') && <div className="grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)_340px] gap-3">
      <section className="flex min-h-0 min-w-0 flex-col overflow-hidden rounded-lg border border-border bg-card">
        <div className="flex min-h-10 shrink-0 items-center gap-2 border-b border-border px-2 py-1">
          <Button size="icon-sm" variant="ghost" aria-label={text('undo')} title={text('undo')} disabled={!draft.canUndo} onClick={draft.undo}><Undo2 /></Button>
          <Button size="icon-sm" variant="ghost" aria-label={text('redo')} title={text('redo')} disabled={!draft.canRedo} onClick={draft.redo}><Redo2 /></Button>
          {view === 'graph' && parsed.definition && <>
            <div className="w-56"><Combobox label={text('addStep')} placeholder={text('addStep')} options={[...index.activities.keys()].sort().map((value) => ({ value, label: value }))} values={[]}
              onValuesChange={(values) => { if (!values[0]) return; const added = addStep(parsed.definition, values[0], { x: 0, y: 0 }); draft.apply(editAll(parsed, added.changes.slice(0, 1))); setSelected(added.id); }} /></div>
            <Button size="xs" variant="ghost" title={text('tidyHelp')} disabled={!Object.keys(parsed.definition.editor?.nodes ?? {}).length} onClick={() => draft.apply(edit(parsed, ['editor', 'nodes'], undefined))}><LayoutGrid />{text('tidy')}</Button>
            <Button size="xs" variant="ghost" aria-pressed={minimap} onClick={() => setMinimap((value) => !value)}><MapIcon />{text('minimap')}</Button>
          </>}
          {view === 'yaml' && <Button size="xs" variant="ghost" disabled={!parsed.definition} title={text('formatHelp')} onClick={() => parsed.definition && draft.apply(toYAML(kind, parsed.definition))}><Wand2 />{text('format')}</Button>}
          <span className="ml-auto flex items-center gap-1.5 text-xs text-muted-foreground" role="status">
            {analysis.pending ? text('checking') : errors.length ? <><CircleAlert className="size-3.5 text-destructive" />{text('problemsCount', { count: errors.length })}</> : parsed.definition ? <><CircleCheck className="size-3.5 text-success" />{text('valid')}</> : null}
          </span>
        </div>
        <div className="min-h-0 flex-1">
          {view === 'yaml' ? <CodeEditor label={text('definition')} value={draft.text} onChange={draft.type} language="yaml" mode={mode} height="fill" hideMessages diagnostics={diagnostics} reveal={reveal} onCursor={setCursor}
            complete={async ({ value, position }) => completeYAML({ kind, index, definition, text: value, position })}
            hover={(position) => hoverText(parsed, position, analysis.analysis)} />
            : <WiringGraph kind={kind} definition={definition} draft={parsed} analysis={analysis.analysis} index={index} mode={mode} problems={problems} run={runSteps.value}
              selected={selected} onSelect={setSelected} onText={draft.apply} minimap={minimap} />}
        </div>
        <footer className="flex shrink-0 flex-wrap items-center gap-2 border-t border-border px-2 py-2">
          {kind === 'rule' && <Input className="h-8 w-56" aria-label={text('ruleName')} placeholder={text('ruleNameRequired')} aria-invalid={!name.trim() && textChanged} value={name} onChange={(event) => setName(event.target.value)} />}
          <Input className="h-8 min-w-40 flex-1" aria-label={text('comment')} placeholder={text('commentPlaceholder')} value={comment} onChange={(event) => setComment(event.target.value)} />
          <Button size="sm" variant="ghost" disabled={!textChanged} onClick={discard}><Undo2 />{text('discardDraft')}</Button>
          <Button size="sm" disabled={!canSave} onClick={save} title={blockedBy ? text(blockedBy) : text('saveShortcut')}><Save />{action.pending ? text('pending') : text('save')}</Button>

          {action.error !== undefined && !conflict && <span className="basis-full text-xs text-destructive" role="alert">{text('mutationFailed')}</span>}
          {!!action.value?.violations.length && <span className="basis-full text-xs text-destructive" role="alert">{text('saveRejected', { count: action.value.violations.length })}</span>}
        </footer>
      </section>
      <aside className="flex min-h-0 min-w-0 flex-col gap-3">
        <Panel flush className="max-h-[40%] min-h-0" title={<><AlertTriangle className="size-4 text-muted-foreground" />{text('problems')}</>} count={shown.length}>
          {!shown.length ? <p className="m-0 flex items-center gap-1.5 px-3 py-2 text-xs text-muted-foreground"><CircleCheck className="size-3.5 text-success" />{parsed.definition ? text('noProblems') : text('loading')}</p>
            : <ul className="m-0 list-none p-0">{shown.map((problem, at) => { const place = lineColumn(draft.text, problem.from); return <li key={at}>
              <button type="button" onClick={() => goTo(problem)} className="flex w-full items-start gap-2 border-b border-border px-3 py-2 text-left text-xs hover:bg-raised">
                {problem.severity === 'error' ? <CircleAlert className="mt-0.5 size-3.5 shrink-0 text-destructive" /> : <Info className="mt-0.5 size-3.5 shrink-0 text-warning" />}
                <span className="min-w-0 flex-1"><span className="block break-words text-foreground">{problem.message}</span>
                  <span className="block truncate font-mono text-2xs text-muted-foreground">{place.line}:{place.column}{problem.path && ` · ${problem.path}`}{problem.code && ` · ${problem.code}`}</span></span>
              </button></li>; })}</ul>}
        </Panel>
        <Panel fill title={<><Info className="size-4 text-muted-foreground" />{text(view === 'graph' ? 'inspector' : 'atCursor')}</>}>
          {view === 'graph' ? <StepInspector kind={kind} definition={definition} step={selected} index={index} analysis={analysis.analysis} run={runSteps.value} mode={mode}
            onEdit={(path, value) => draft.apply(edit(parsed, path, value))} onChanges={(changes) => draft.apply(editAll(parsed, changes))} onText={draft.apply} onSelect={setSelected} />
            : <CursorContext kind={kind} parsed={parsed} cursor={cursor} analysis={analysis.analysis} index={index} definition={definition} />}
        </Panel>
      </aside>
    </div>}
    {view === 'versions' && subject && <div className="flex min-h-0 flex-1 flex-col"><Versions subject={subject} current={loadedVersion || undefined} managed={!!managedBy} mode={mode} onChanged={reload}
      onOpenDraft={(text) => { draft.apply(text); onView('yaml'); }} /></div>}
    {view === 'test' && <ExecutionGate kind={kind}><div className="flex min-h-0 flex-1 flex-col"><TestRun subject={subject ?? { kind: 'rule', id: '' }} draft={parsed.definition} dirty={changed} mode={mode}
      schema={kind === 'binding' ? index.hooks.get(source)?.value.input : index.events.get(source)?.value.schema}
      onRun={(ref) => { setRun(ref); }} />
      {run && <div className="mt-2"><Button size="sm" variant="outline" onClick={() => onView('graph')}><GitFork />{text('showOnGraph')}</Button></div>}</div></ExecutionGate>}
    {view === 'runs' && subject && <ExecutionGate kind={kind}><div className="flex min-h-0 flex-1 flex-col"><Runs subject={subject} mode={mode} onOpen={(ref) => { setRun(ref); onView('graph'); }} /></div></ExecutionGate>}
    <DetailDrawer open={conflictOpen} onOpenChange={setConflictOpen} size="xl" title={text('compareLatest')} description={text('compareLatestHelp')}>
      {conflictOpen && <LatestDiff kind={kind} target={target} draft={draft.text} mode={mode} />}
    </DetailDrawer>
  </div>;
}

const bannerColors = {
  danger: 'border-destructive/40 bg-destructive/10 text-destructive',
  warning: 'border-warning/40 bg-warning/10 text-warning',
  info: 'border-border bg-raised text-foreground',
};

function Banner({ tone, icon, title, actions, children }: { tone: keyof typeof bannerColors; icon: ReactNode; title: ReactNode; actions?: ReactNode; children?: ReactNode }) {
  const colors = bannerColors[tone];
  return <div role="status" className={`mb-3 flex shrink-0 flex-wrap items-start gap-3 rounded-lg border px-3 py-2 text-sm ${colors} [&>svg]:mt-0.5 [&>svg]:size-4 [&>svg]:shrink-0`}>
    {icon}<div className="min-w-0 flex-1"><div className="font-medium">{title}</div>{children && <div className="text-xs text-foreground/80">{children}</div>}</div>
    {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
  </div>;
}

function HeaderActions({ target, loaded, saved, managed, loadedVersion, onChanged, onDeleted }: { target: WiringTarget; loaded: Loaded; saved: boolean; managed: boolean; loadedVersion: bigint; onChanged: () => void; onDeleted: () => void }) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText(), action = usePlatformAction<unknown>();
  const control = (execute: (signal: AbortSignal) => Promise<unknown>, after: () => void) => async (signal: AbortSignal) => {
    const result = await action.run((abort) => execute(AbortSignal.any([signal, abort]))); if (result === undefined) throw new Error('Outcome is unavailable'); after();
  };
  if (!saved || (managed && target.kind === 'binding')) return null;
  if (target.kind === 'binding') return <ConfirmAction trigger={<><Trash2 className="size-3.5" />{text('delete')}</>} title={text('deleteBindingConfirm')} description={text('deleteBindingHelp')} disabled={action.pending || action.disabled}
    onConfirm={control((signal) => bindings.deleteBinding(create(api.DeleteBindingRequestSchema, { hook: target.hook, baseVersion: loadedVersion }), { signal }), onChanged)} />;
  const id = target.id, paused = loaded.rule?.paused;
  return <>
    {paused ? <ConfirmAction trigger={<><Play className="size-3.5" />{text('resume')}</>} title={text('resumeRuleConfirm')} description={text('resumeRuleHelp')} disabled={action.pending || action.disabled}
      onConfirm={control((signal) => rules.resumeRule(create(api.ResumeRuleRequestSchema, { id, baseVersion: loadedVersion }), { signal }), onChanged)} />
      : <ConfirmAction trigger={<><Pause className="size-3.5" />{text('pause')}</>} title={text('pauseRuleConfirm')} description={text('pauseRuleHelp')} disabled={action.pending || action.disabled}
        onConfirm={control((signal) => rules.pauseRule(create(api.PauseRuleRequestSchema, { id, baseVersion: loadedVersion }), { signal }), onChanged)} />}
    {!managed && <ConfirmAction trigger={<><Trash2 className="size-3.5" />{text('delete')}</>} title={text('deleteRuleConfirm')} description={text('deleteRuleHelp')} disabled={action.pending || action.disabled}
      onConfirm={control((signal) => rules.deleteRule(create(api.DeleteRuleRequestSchema, { id, baseVersion: loadedVersion }), { signal }), onDeleted)} />}
    {action.error !== undefined && <span className="text-xs text-destructive" role="alert">{text('mutationFailed')}</span>}
  </>;
}

/** The latest saved definition against the draft. */
function LatestDiff({ kind, target, draft, mode }: { kind: WiringKind; target: WiringTarget; draft: string; mode: Mode }) {
  const bindings = useClient(api.BindingServiceClient), rules = useClient(api.RuleServiceClient), text = usePlatformText();
  const state = usePlatformQuery(`wiring-latest:${targetKey(target)}`, async (signal) => {
    const version = target.kind === 'binding' ? (await bindings.getBinding(create(api.GetBindingRequestSchema, { hook: target.hook }), { signal })).version
      : target.id ? (await rules.getRule(create(api.GetRuleRequestSchema, { id: target.id }), { signal })).version : undefined;
    return version?.definition ? toYAML(kind, version.definition) : '';
  });
  return state.value === undefined ? <p className="text-sm text-muted-foreground">{text('loading')}</p>
    : <DiffViewer label={text('compareLatest')} before={state.value} after={draft} language="yaml" mode={mode} height={560} />;
}

/** The type and schema of the value under the cursor. */
function hoverText(draft: Draft, position: number, analysis?: api.Analysis): string | undefined {
  const { path } = pathAt(draft.doc, position);
  const type = analysis?.types.find((entry) => entry.path === path)?.type;
  return type ? `${path}: ${type}` : undefined;
}

function CursorContext({ kind, parsed, cursor, analysis, index, definition }: { kind: WiringKind; parsed: Draft; cursor: number; analysis?: api.Analysis; index: WiringIndex; definition?: Definition }) {
  const text = usePlatformText();
  const { path } = pathAt(parsed.doc, cursor);
  if (!path) return <p className="m-0 text-xs text-muted-foreground">{text('atCursorHelp')}</p>;
  const segments = path.split('/').slice(1).map(unescape);
  const type = analysis?.types.find((entry) => entry.path === path)?.type;
  const reads = analysis?.references.filter((entry) => entry.path === path) ?? [];
  // The step the cursor is in, through the for-each bodies it is nested in.
  const frames = framesOf(segments, definition?.steps ?? {}), chain = frames.chain.map((entry) => entry.name);
  const step = chain.at(-1), parent = chain.slice(0, -1).join('/'), current = frames.chain.at(-1)?.step as api.Step | undefined;
  const stepAnalysis = step ? analysis?.steps.find((entry) => entry.name === step && entry.parent === parent) : undefined;
  let expected: string | undefined, description: string | undefined;
  if (current && (frames.rest[0] === 'input' || frames.rest[0] === 'undoInput')) {
    const activity = frames.rest[0] === 'input' ? current.activity : current.undo;
    const shape = shapeAt(shapeOf(activity ? index.activities.get(activity)?.value.input : undefined), frames.rest.slice(1));
    expected = shape && shapeLabel(shape);
    const fields = shapeAt(shapeOf(activity ? index.activities.get(activity)?.value.input : undefined), frames.rest.slice(1, -1));
    description = fields?.fields?.find((field) => field.name === frames.rest.at(-1))?.description;
  } else if (definition && kind === 'binding' && segments[0] === 'result') {
    const shape = shapeAt(shapeOf(index.hooks.get((definition as api.BindingDefinition).hook)?.value.output), segments.slice(1));
    expected = shape && shapeLabel(shape);
  }
  const found = nodeAt(parsed.doc, path).found;
  return <dl className="m-0 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-xs">
    <dt className="text-muted-foreground">{text('path')}</dt><dd className="m-0 break-all font-mono">{path}{!found && ' ?'}</dd>
    {type && <><dt className="text-muted-foreground">{text('celType')}</dt><dd className="m-0 font-mono">{type}</dd></>}
    {expected && <><dt className="text-muted-foreground">{text('expectedType')}</dt><dd className="m-0 font-mono">{expected}</dd></>}
    {description && <><dt className="text-muted-foreground">{text('description')}</dt><dd className="m-0">{description}</dd></>}
    {!!reads.length && <><dt className="text-muted-foreground">{text('reads')}</dt><dd className="m-0 flex flex-wrap gap-1">{reads.map((entry, at) => <Badge key={at} variant="outline" className="font-mono">{[entry.variable, ...entry.fields].join('.')}</Badge>)}</dd></>}
    {stepAnalysis?.itemType && <><dt className="text-muted-foreground">{text('itemType')}</dt><dd className="m-0 font-mono">{stepAnalysis.itemType}</dd></>}
    {stepAnalysis && <><dt className="text-muted-foreground">{text('level')}</dt><dd className="m-0 font-mono">{stepAnalysis.level < 0 ? '—' : stepAnalysis.level}</dd>
      <dt className="text-muted-foreground">{text('dependsOn')}</dt><dd className="m-0 font-mono">{[...new Set([...stepAnalysis.data, ...stepAnalysis.after, ...stepAnalysis.when])].join(', ') || '—'}</dd></>}
  </dl>;
}
