import { useEffect, useState, type ReactNode } from 'react';
import { create, toJson } from '@bufbuild/protobuf';
import { ValueSchema } from '@bufbuild/protobuf/wkt';
import * as api from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { Badge, Button, Combobox, Input, SectionLabel, StatusBadge, Textarea } from '@backplane-labs/backplane-ui';
import { InlineCodeEditor, JSONViewer } from '@backplane-labs/backplane-editors';
import { Repeat, Trash2 } from 'lucide-react';
import { usePlatformAction } from '../runtime.js';
import { usePlatformText } from '../locales.js';
import { enumLabel } from '../format.js';
import { scopeAt, scopeOf, type Scope, type WiringIndex } from './catalog.js';
import { completeCEL, definitionSource } from './complete.js';
import { removeStep, stepAt, stepPathOf, toYAML, validStepName, type Change, type Definition, type WiringKind } from './document.js';
import { stepTone } from './history.js';
import { shapeLabel, shapeOf } from './shape.js';

export interface StepInspectorProps {
  kind: WiringKind; definition?: Definition; step?: string; index: WiringIndex; analysis?: api.Analysis; run?: api.StepRun[]; mode?: 'dark' | 'light';
  onEdit: (path: readonly (string | number)[], value: unknown) => void; onChanges: (changes: Change[]) => void; onText: (text: string) => void; onSelect: (step: string | undefined) => void;
}

/** The selected node of the graph as a form; edits go to the draft as YAML edits. */
export function StepInspector(props: StepInspectorProps) {
  const { kind, definition, step } = props, text = usePlatformText();
  if (!definition) return <p className="m-0 text-xs text-muted-foreground">{text('graphNeedsYaml')}</p>;
  if (step === '$trigger') return <TriggerForm {...props} definition={definition} />;
  if (step === '$result' && kind === 'binding') return <ValueFields {...props} definition={definition} base={['result']} title={text('resultValue')}
    scope={scopeAt(props.index, definitionSource(kind, definition)!, definition.steps, ['result'], kind)}
    schema={props.index.hooks.get((definition as api.BindingDefinition).hook)?.value.output} value={json((definition as api.BindingDefinition).result)} />;
  if (!step || !stepAt(definition, step).step) return <Overview {...props} definition={definition} />;
  return <StepForm key={step} {...props} definition={definition} id={step} />;
}

const json = (value?: api.BindingDefinition['result']): unknown => value ? toJson(ValueSchema, value) : undefined;

function Field({ label, hint, children }: { label: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return <label className="grid grid-cols-[minmax(0,1fr)] gap-1 text-xs"><span className="flex items-baseline gap-2 text-muted-foreground">{label}{hint && <span className="ml-auto truncate text-2xs text-muted-foreground">{hint}</span>}</span>{children}</label>;
}

/** A text input that commits on blur or Enter, not on every keystroke (one undo step per edit). */
function CommitInput({ value, onCommit, ...props }: { value: string; onCommit: (value: string) => void } & Omit<Parameters<typeof Input>[0], 'value' | 'onChange'>) {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  return <Input {...props} value={draft} onChange={(event) => setDraft(event.target.value)} onBlur={() => { if (draft !== value) onCommit(draft); }}
    onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); if (draft !== value) onCommit(draft); } }} />;
}

/** A CEL expression edited inline with completion; commits when it loses focus. */
function CelInput({ label, value, onCommit, scope, mode }: { label: string; value: string; onCommit: (value: string) => void; scope: Scope; mode?: 'dark' | 'light' }) {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  return <div onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null) && draft !== value) onCommit(draft); }}>
    <InlineCodeEditor label={label} value={draft} onChange={setDraft} language="cel" mode={mode ?? 'dark'} hideMessages onSubmit={() => { if (draft !== value) onCommit(draft); }}
      complete={async ({ value: current, position }) => completeCEL(current.slice(0, position), position, scope)} />
  </div>;
}

function Overview({ kind, definition, index, mode, onEdit }: StepInspectorProps & { definition: Definition }) {
  const text = usePlatformText(), source = definitionSource(kind, definition)!;
  return <div className="grid grid-cols-[minmax(0,1fr)] gap-3">
    <p className="m-0 text-xs text-muted-foreground">{text('inspectorHelp')}</p>
    <Field label={text('description')}><Textarea className="min-h-16 text-xs" defaultValue={definition.description} onBlur={(event) => { if (event.target.value !== definition.description) onEdit(['description'], event.target.value || undefined); }} /></Field>
    {kind === 'rule' && <Field label={text('when')} hint={text('ruleWhenHelp')}>
      <CelInput label={text('when')} value={(definition as api.RuleDefinition).when} scope={scopeOf(index, source, {}, true)} mode={mode} onCommit={(value) => onEdit(['when'], value || undefined)} /></Field>}
    <div className="grid gap-1 text-xs"><SectionLabel>{text('stepsTitle')}</SectionLabel>
      <span className="text-muted-foreground">{text('stepsCount', { count: Object.keys(definition.steps).length })}</span></div>
  </div>;
}

function TriggerForm({ kind, definition, index, mode, onEdit }: StepInspectorProps & { definition: Definition }) {
  const text = usePlatformText(), source = definitionSource(kind, definition)!;
  const schema = kind === 'binding' ? index.hooks.get((source as { hook: string }).hook)?.value.input : index.events.get((source as { event: string }).event)?.value.schema;
  const fields = shapeOf(schema).fields ?? [];
  return <div className="grid grid-cols-[minmax(0,1fr)] gap-3">
    <Field label={text(kind === 'binding' ? 'hook' : 'event')}><span className="font-mono text-sm">{'hook' in source ? source.hook : source.event}</span></Field>
    {kind === 'rule' && <Field label={text('when')} hint={text('ruleWhenHelp')}>
      <CelInput label={text('when')} value={(definition as api.RuleDefinition).when} scope={scopeOf(index, source, {}, true)} mode={mode} onCommit={(value) => onEdit(['when'], value || undefined)} /></Field>}
    <SectionLabel>{text(kind === 'binding' ? 'hookInput' : 'eventInput')}</SectionLabel>
    {!fields.length ? <p className="m-0 text-xs text-muted-foreground">{text('noSchema')}</p> : <ul className="m-0 grid list-none gap-1 p-0 text-xs">{fields.map((field) =>
      <li key={field.name} className="flex items-baseline gap-2"><span className="font-mono">{field.name}{field.required && <span className="text-destructive">*</span>}</span><span className="font-mono text-muted-foreground">{shapeLabel(field.shape)}</span>
        {field.description && <span className="min-w-0 truncate text-muted-foreground" title={field.description}>{field.description}</span>}</li>)}</ul>}
  </div>;
}

/** The fields of a value (a step's input, a binding's result) against its schema: one CEL input per field. */
function ValueFields({ mode, onEdit, base, title, schema, value, scope }: StepInspectorProps & { definition: Definition; base: (string | number)[]; title: string; schema?: Parameters<typeof shapeOf>[0]; value: unknown; scope: Scope }) {
  const text = usePlatformText();
  const fields = shapeOf(schema).fields ?? [], object = value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
  const extra = object ? Object.keys(object).filter((name) => !fields.some((field) => field.name === name)) : [];
  return <div className="grid grid-cols-[minmax(0,1fr)] gap-2">
    <SectionLabel>{title}</SectionLabel>
    {typeof value === 'string' && <Field label={text('wholeValue')} hint={text('wholeValueHelp')}><CelInput label={title} value={value} scope={scope} mode={mode} onCommit={(next) => onEdit(base, next || undefined)} /></Field>}
    {!schema && typeof value !== 'string' && <p className="m-0 text-xs text-muted-foreground">{text('noSchema')}</p>}
    {typeof value !== 'string' && [...fields.map((field) => ({ name: field.name, type: shapeLabel(field.shape), required: field.required, description: field.description })), ...extra.map((name) => ({ name, type: '?', required: false, description: text('undeclaredField') }))].map((field) => {
      const current = object?.[field.name];
      return <Field key={field.name} label={<span className="font-mono text-foreground">{field.name}{field.required && <span className="text-destructive">*</span>} <span className="text-muted-foreground">{field.type}</span></span>} hint={field.description}>
        {current === undefined || typeof current === 'string'
          ? <CelInput label={field.name} value={current ?? ''} scope={scope} mode={mode} onCommit={(next) => onEdit([...base, field.name], next || undefined)} />
          : <div className="rounded-md border border-border bg-raised px-2 py-1 font-mono text-2xs text-muted-foreground" title={text('editInYaml')}>{JSON.stringify(current)}</div>}
      </Field>;
    })}
  </div>;
}

/** scope without a variable: a step does not read its own output. */
function without(scope: Scope, name: string): Scope {
  const variables = new Map(scope.variables);
  variables.delete(name);
  return { variables };
}

/** A small number: empty is the default (the placeholder). */
function NumberField({ label, title, value, placeholder, onCommit }: { label: string; title?: string; value: number; placeholder: string; onCommit: (value: number | undefined) => void }) {
  return <Field label={label}><CommitInput className="h-8 text-xs" inputMode="numeric" aria-label={label} title={title} placeholder={placeholder} value={value ? String(value) : ''}
    onCommit={(next) => onCommit(next && Number.isFinite(Number(next)) ? Number(next) : undefined)} /></Field>;
}

/** A step's for-each options: its list, item, concurrency, error policy and limit; a body's result. */
function ForEachFields(props: StepInspectorProps & { definition: Definition; id: string; current: api.Step; scopeFor: (place: string) => Scope }) {
  const { id, current: step, mode, onEdit, onChanges, onSelect, scopeFor } = props, text = usePlatformText(), base = stepPathOf(id);
  const body = Object.keys(step.steps).sort(), item = step.as || 'item', result = json(step.result);
  if (!step.forEach) return <Button size="sm" variant="outline" className="justify-self-start" onClick={() => onEdit([...base, 'forEach'], '[]')}><Repeat />{text('makeForEach')}</Button>;
  return <div className="grid min-w-0 grid-cols-[minmax(0,1fr)] gap-2 rounded-md border border-info/30 bg-info/5 p-2">
    <SectionLabel className="flex items-center gap-1.5"><Repeat className="size-3.5 text-info" />{text('forEachTitle')}</SectionLabel>
    <Field label={text('forEachList')} hint={text('forEachHelp')}><CelInput label={text('forEachList')} value={step.forEach} scope={scopeFor('forEach')} mode={mode} onCommit={(value) => onEdit([...base, 'forEach'], value || '[]')} /></Field>
    <Field label={text('itemVar')} hint={text('itemVarHelp', { name: item })}><CommitInput className="h-8 font-mono text-xs" aria-label={text('itemVar')} placeholder="item" value={step.as}
      onCommit={(value) => { if (!value || validStepName(value)) onEdit([...base, 'as'], value || undefined); }} /></Field>
    <div className="grid grid-cols-2 gap-2 [&>*]:min-w-0">
      <NumberField label={text('concurrency')} value={step.concurrency} placeholder="10" onCommit={(value) => onEdit([...base, 'concurrency'], value)} />
      <NumberField label={text('maxItems')} title={text('maxItemsHelp', { count: 1000 })} value={step.maxItems} placeholder="1000" onCommit={(value) => onEdit([...base, 'maxItems'], value)} />
    </div>
    <div className="grid gap-1 text-xs"><span className="text-muted-foreground">{text('onError')}</span>
      <div className="flex flex-wrap gap-1">{([['fail', 'onErrorFail', 'onErrorFailHelp'], ['continue', 'onErrorContinue', 'onErrorContinueHelp']] as const).map(([value, label, help]) => {
        const on = (step.onError || 'fail') === value;
        return <button key={value} type="button" aria-pressed={on} title={text(help)} onClick={() => onEdit([...base, 'onError'], value === 'fail' ? undefined : value)}
          className="h-6 rounded-sm border border-border px-2 text-2xs text-muted-foreground aria-pressed:border-primary/50 aria-pressed:bg-primary/10 aria-pressed:text-foreground">{text(label)}</button>;
      })}</div>
    </div>
    {!!body.length && <>
      <div className="grid gap-1 text-xs"><SectionLabel>{text('stepsTitle')}</SectionLabel>
        <div className="flex flex-wrap gap-1">{body.map((child) => <button key={child} type="button" onClick={() => onSelect(`${id}/${child}`)}
          className="h-6 rounded-sm border border-border px-2 font-mono text-2xs text-link hover:border-border-strong">{child}</button>)}</div></div>
      {result === undefined || typeof result === 'string'
        ? <Field label={text('bodyResult')} hint={text('bodyResultHelp')}><CelInput label={text('bodyResult')} value={result ?? ''} scope={scopeFor('result')} mode={mode} onCommit={(value) => onEdit([...base, 'result'], value || undefined)} /></Field>
        : <ValueFields {...props} base={[...base, 'result']} title={text('bodyResult')} value={result} scope={scopeFor('result')} />}
    </>}
    {!body.length && <Button size="sm" variant="ghost" className="justify-self-start" onClick={() => onChanges(['forEach', 'as', 'concurrency', 'onError', 'maxItems'].map((key) => ({ path: [...base, key], value: undefined })))}>{text('stopForEach')}</Button>}
  </div>;
}

function StepForm(props: StepInspectorProps & { definition: Definition; id: string }) {
  const { kind, definition, id, index, analysis, run, mode, onEdit, onChanges, onText, onSelect } = props;
  const wiring = useClient(api.WiringServiceClient), text = usePlatformText(), rename = usePlatformAction<api.RenameStepResponse>();
  const { step: found, siblings } = stepAt(definition, id), step = found!, source = definitionSource(kind, definition)!;
  const name = id.split('/').at(-1)!, parent = id.includes('/') ? id.slice(0, id.lastIndexOf('/')) : '', base = stepPathOf(id);
  const at = (...rest: string[]) => [...base, ...rest];
  const scopeFor = (place: string) => without(scopeAt(index, source, definition.steps, at(place), kind), name);
  const activity = index.activities.get(step.activity)?.value, undo = index.activities.get(step.undo)?.value;
  const facts = analysis?.steps.find((entry) => entry.name === name && entry.parent === parent);
  const calls = run?.filter((call) => call.step === name && call.parent === parent) ?? [];
  const others = Object.keys(siblings).filter((other) => other !== name).sort();
  const activities = [...index.activities.keys()].sort().map((full) => ({ value: full, label: full }));
  const hasBody = Object.keys(step.steps).length > 0;
  const doRename = (to: string) => void rename.run(async (signal) => {
    const response = await wiring.renameStep(create(api.RenameStepRequestSchema, { from: id, to, definition: kind === 'binding' ? { case: 'binding', value: definition as api.BindingDefinition } : { case: 'rule', value: definition as api.RuleDefinition } }), { signal });
    if (response.definition.value) { onText(toYAML(kind, response.definition.value)); onSelect(parent ? `${parent}/${to}` : to); }
    return response;
  });
  const retry = step.retry, duration = (value?: { seconds: bigint; nanos: number }) => value ? `${Number(value.seconds) + value.nanos / 1e9}s` : '';
  return <div className="grid grid-cols-[minmax(0,1fr)] gap-3">
    {parent && <button type="button" className="justify-self-start text-2xs text-link hover:underline" onClick={() => onSelect(parent)}>{text('inLoop', { name: parent })}</button>}
    <Field label={text('stepName')} hint={facts && facts.level >= 0 ? text('levelN', { level: facts.level }) : undefined}>
      <CommitInput className="h-8 font-mono text-xs" aria-label={text('stepName')} value={name} onCommit={(to) => { if (validStepName(to)) doRename(to); }} />
    </Field>
    {!!rename.value?.violations.length && <p className="m-0 text-xs text-destructive" role="alert">{rename.value.violations[0]!.message}</p>}
    {!hasBody && <Field label={text('activity')} hint={activity?.description}>
      <Combobox label={text('activity')} options={activities} values={step.activity ? [step.activity] : []} onValuesChange={(values) => { if (values[0]) onEdit(at('activity'), values[0]); }} placeholder={text('pickActivity')} />
    </Field>}
    <Field label={text('description')}><CommitInput className="h-8 text-xs" aria-label={text('description')} value={step.description} onCommit={(value) => onEdit(at('description'), value || undefined)} /></Field>
    <ForEachFields {...props} id={id} current={step} scopeFor={scopeFor} />
    <Field label={text('when')} hint={text('whenHelp')}><CelInput label={text('when')} value={step.when} scope={scopeFor('when')} mode={mode} onCommit={(value) => onEdit(at('when'), value || undefined)} /></Field>
    {!hasBody && <ValueFields {...props} base={at('input')} title={text('input')} schema={activity?.input} value={json(step.input)} scope={scopeFor('input')} />}
    <div className="grid gap-1 text-xs"><SectionLabel>{text('runsAfter')}</SectionLabel>
      <div className="flex flex-wrap gap-1">{others.length ? others.map((other) => { const on = step.after.includes(other); return <button key={other} type="button" aria-pressed={on}
        onClick={() => onEdit(at('after'), on ? step.after.filter((entry) => entry !== other) : [...step.after, other])}
        className="h-6 rounded-sm border border-border px-2 font-mono text-2xs text-muted-foreground aria-pressed:border-primary/50 aria-pressed:bg-primary/10 aria-pressed:text-foreground">{other}</button>; })
        : <span className="text-muted-foreground">{text('noOtherSteps')}</span>}</div>
      {facts && <span className="text-2xs text-muted-foreground">{text('dependsOn')}: <span className="font-mono">{[...new Set([...facts.data, ...facts.after, ...facts.when])].join(', ') || '—'}</span></span>}
    </div>
    {!hasBody && <>
      <Field label={text('undoActivity')} hint={text('undoHelp')}>
        <Combobox label={text('undoActivity')} options={activities} values={step.undo ? [step.undo] : []} onValuesChange={(values) => onEdit(at('undo'), values[0] || undefined)} placeholder={text('none')} />
      </Field>
      {step.undo && <ValueFields {...props} base={at('undoInput')} title={text('undoInput')} schema={undo?.input} value={json(step.undoInput)} scope={scopeFor('undoInput')} />}
      <div className="grid grid-cols-2 gap-2 [&>*]:min-w-0">
        <Field label={text('attempts')}><CommitInput className="h-8 text-xs" inputMode="numeric" placeholder={String(activity?.retry?.attempts || 3)} value={retry?.attempts ? String(retry.attempts) : ''} onCommit={(value) => onEdit(at('retry', 'attempts'), value ? Number(value) : undefined)} /></Field>
        <Field label={text('backoff')}><CommitInput className="h-8 text-xs" inputMode="decimal" placeholder="2" value={retry?.backoff ? String(retry.backoff) : ''} onCommit={(value) => onEdit(at('retry', 'backoff'), value ? Number(value) : undefined)} /></Field>
        <Field label={text('initialInterval')}><CommitInput className="h-8 text-xs" placeholder="1s" value={duration(retry?.initialInterval)} onCommit={(value) => onEdit(at('retry', 'initialInterval'), value || undefined)} /></Field>
        <Field label={text('maxInterval')}><CommitInput className="h-8 text-xs" placeholder="30s" value={duration(retry?.maxInterval)} onCommit={(value) => onEdit(at('retry', 'maxInterval'), value || undefined)} /></Field>
        <Field label={text('startToClose')}><CommitInput className="h-8 text-xs" placeholder={activity?.startToClose ? duration(activity.startToClose) : '30s'} value={duration(step.startToClose)} onCommit={(value) => onEdit(at('startToClose'), value || undefined)} /></Field>
        <Field label={text('heartbeat')}><CommitInput className="h-8 text-xs" placeholder={activity?.heartbeat ? duration(activity.heartbeat) : '—'} value={duration(step.heartbeat)} onCommit={(value) => onEdit(at('heartbeat'), value || undefined)} /></Field>
      </div>
    </>}
    {!!calls.length && <div className="grid gap-2"><SectionLabel>{text('lastRun')}</SectionLabel>{calls.map((call, position) => <div key={position} className="grid gap-1 rounded-md border border-border p-2 text-xs">
      <div className="flex items-center gap-2"><StatusBadge tone={stepTone[call.status]}>{enumLabel(api.StepRunStatus, call.status)}</StatusBadge>{call.undo && <Badge variant="outline">undo</Badge>}
        {call.item !== undefined && <Badge variant="outline" className="font-mono">{text('itemN', { item: call.item })}</Badge>}
        <span className="font-mono text-link">{call.activity}</span>{call.attempt > 1 && <span className="ml-auto text-muted-foreground">{text('attemptShort', { attempt: call.attempt })}</span>}</div>
      {call.error && <div className="font-mono text-destructive">{call.error}</div>}
      {call.input && <JSONViewer label={text('input')} value={call.input} mode={mode ?? 'dark'} height={90} />}
      {call.output && <JSONViewer label={text('output')} value={call.output} mode={mode ?? 'dark'} height={90} />}
    </div>)}</div>}
    <Button size="sm" variant="outline" className="justify-self-start text-destructive" onClick={() => { onChanges(removeStep(definition, id)); onSelect(undefined); }}><Trash2 />{text('deleteStep')}</Button>
  </div>;
}
