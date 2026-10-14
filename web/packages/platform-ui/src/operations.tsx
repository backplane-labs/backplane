import { nativeJSON } from './serialization.js';
export { nativeJSON } from './serialization.js';
import { useMemo, useState } from 'react';
import { create, toJsonString } from '@bufbuild/protobuf';
import { structToNative, type Schema } from '@gopherex/schemapb';
import * as api from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { Button, EmptyState, SelectControl, Panel, StatusBadge } from '@backplane-labs/backplane-ui';
import { CodeEditor, JSONViewer } from '@backplane-labs/backplane-editors';
import { SchemaForm } from '@backplane-labs/backplane-schema-forms';
import { Link2, Play, Radio, TerminalSquare, Workflow, Zap } from 'lucide-react';
import { QueryState, usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';

export type OperationKind = 'hook' | 'activity' | 'event' | 'workflow';
export function OperationSelector({ service, kind, value, onChange }: { service: string; kind: OperationKind; value: string; onChange: (value: string) => void }) {
  const client = useClient(api.CatalogServiceClient), text = usePlatformText();
  const state = usePlatformQuery(`operations:${service}`, (signal) => client.getService(create(api.GetServiceRequestSchema, { name: service }), { signal }));
  const definitions = state.value?.latest?.[kind === 'hook' ? 'hooks' : kind === 'activity' ? 'activities' : kind === 'event' ? 'events' : 'workflows'] ?? [];
  return <QueryState state={state}><SelectControl aria-label={text(kind)} value={value} onValueChange={onChange} options={[{ value: '', label: text('select') },
    ...definitions.map((definition) => ({ value: kind === 'workflow' ? definition.name : `${service}.${definition.name}`, label: definition.name }))]} /></QueryState>;
}

const kinds: { kind: OperationKind; icon: typeof Link2 }[] = [{ kind: 'hook', icon: Link2 }, { kind: 'activity', icon: Zap }, { kind: 'event', icon: Radio }, { kind: 'workflow', icon: Workflow }];
type Outcome = { ok: boolean; body: string; took: number };

/** Call a hook, run an activity, publish a test event or start a workflow of one service. */
export function ServiceOperations({ service, mode }: { service: string; mode: 'dark' | 'light' }) {
  const catalog = useClient(api.CatalogServiceClient), calls = useClient(api.CallServiceClient), events = useClient(api.EventServiceClient), workflows = useClient(api.WorkflowServiceClient), text = usePlatformText();
  const state = usePlatformQuery(`operations:${service}`, (signal) => catalog.getService(create(api.GetServiceRequestSchema, { name: service }), { signal }));
  const [kind, setKind] = useState<OperationKind>('hook'), [name, setName] = useState(''), [input, setInput] = useState('{}');
  const action = usePlatformAction<Outcome>();
  const options = useMemo<{ name: string; description?: string; input?: Schema }[]>(() => {
    const manifest = state.value?.latest; if (!manifest) return [];
    return kind === 'event' ? manifest.events.map((event) => ({ name: event.name, description: event.description, input: event.schema })) : kind === 'hook' ? manifest.hooks : kind === 'activity' ? manifest.activities : manifest.workflows;
  }, [state.value, kind]);
  const selected = options.find((option) => option.name === name) ?? options[0];
  const execute = async (payload: string, signal: AbortSignal): Promise<Outcome> => {
    const started = performance.now(), target = selected!.name;
    const body = kind === 'hook' ? toJsonString(api.CallHookResponseSchema, await calls.callHook(create(api.CallHookRequestSchema, { hook: `${service}.${target}`, input: payload }), { signal }), { prettySpaces: 2 })
      : kind === 'activity' ? toJsonString(api.RunActivityResponseSchema, await calls.runActivity(create(api.RunActivityRequestSchema, { activity: `${service}.${target}`, input: payload }), { signal }), { prettySpaces: 2 })
      : kind === 'event' ? toJsonString(api.PublishTestEventResponseSchema, await events.publishTestEvent(create(api.PublishTestEventRequestSchema, { event: `${service}.${target}`, payload }), { signal }), { prettySpaces: 2 })
      : toJsonString(api.StartWorkflowResponseSchema, await workflows.startWorkflow(create(api.StartWorkflowRequestSchema, { service, workflow: target, input: payload }), { signal }), { prettySpaces: 2 });
    const parsed = JSON.parse(body) as { result?: { error?: string } };
    return { ok: !parsed.result?.error, body, took: performance.now() - started };
  };
  return <QueryState state={state}><div className="grid h-full min-h-0 gap-4 xl:grid-rows-[minmax(0,1fr)] xl:grid-cols-[320px_minmax(0,1fr)]">
    <Panel fill title={<><TerminalSquare className="size-4 text-muted-foreground" />{text('operations')}</>} flush>
      <div className="grid grid-cols-4 gap-1 border-b border-border p-2" role="group" aria-label={text('selectAction')}>
        {kinds.map(({ kind: entry, icon: Icon }) => <button key={entry} type="button" aria-pressed={kind === entry} disabled={action.pending} onClick={() => { setKind(entry); setName(''); }}
          className="flex flex-col items-center gap-1 rounded-md py-1.5 text-2xs text-muted-foreground hover:bg-raised hover:text-foreground aria-pressed:bg-primary/10 aria-pressed:text-foreground"><Icon className="size-4" />{text(entry)}</button>)}
      </div>
      {!options.length && <EmptyState className="py-6" title={text('noOperations')} />}
      {options.map((option) => <button key={option.name} type="button" aria-pressed={selected?.name === option.name} disabled={action.pending} onClick={() => setName(option.name)}
        className="block w-full border-b border-border px-3 py-2 text-left last:border-b-0 hover:bg-raised aria-pressed:bg-primary/10">
        <span className="block truncate font-mono text-xs">{option.name}</span>{option.description && <span className="block truncate text-2xs text-muted-foreground">{option.description}</span>}
      </button>)}
    </Panel>
    <div className="flex min-h-0 min-w-0 flex-col gap-4 overflow-auto">
      {selected && <Panel title={<span className="font-mono">{kind === 'workflow' ? selected.name : `${service}.${selected.name}`}</span>} description={text(`operationHelp_${kind}`)}>
        {selected.input ? <SchemaForm key={`${service}:${kind}:${selected.name}`} schema={selected.input} label={text('input')} submitLabel={text('execute')} disabled={action.disabled || action.pending} onSubmit={async (baked, signal) => {
          const response = await action.run((abort) => execute(nativeJSON(structToNative(baked.values)), AbortSignal.any([signal, abort])));
          if (response === undefined) throw new Error('Operation outcome is unavailable');
        }} /> : <div className="grid gap-3"><CodeEditor label={text('input')} value={input} onChange={setInput} language="json" mode={mode} disabled={action.pending} height={160} />
          <div><Button size="sm" disabled={action.pending || action.disabled} onClick={() => void action.run((signal) => execute(input, signal))}><Play />{action.pending ? text('pending') : text('execute')}</Button></div></div>}
      </Panel>}
      {(action.value || action.error !== undefined) && <Panel title={text('output')} actions={action.value && <><StatusBadge tone={action.value.ok ? 'success' : 'danger'}>{text(action.value.ok ? 'succeeded' : 'failed')}</StatusBadge><span className="text-xs text-muted-foreground">{Math.round(action.value.took)}ms</span></>}>
        {action.error !== undefined ? <p className="m-0 text-sm text-destructive" role="alert">{text('mutationFailed')}</p> : <JSONViewer label={text('output')} value={action.value!.body} mode={mode} />}
      </Panel>}
    </div>
  </div></QueryState>;
}
