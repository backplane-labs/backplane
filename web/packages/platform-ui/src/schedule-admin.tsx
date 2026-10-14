import { useState } from 'react';
import { create } from '@bufbuild/protobuf';
import { DurationSchema } from '@bufbuild/protobuf/wkt';
import * as api from '@backplane-labs/backplane-api';
import { useClient } from '@backplane-labs/backplane-react';
import { parse, parseNumberAndBigInt } from 'lossless-json';
import { structToNative, type NativeStruct } from '@gopherex/schemapb';
import { SchemaForm } from '@backplane-labs/backplane-schema-forms';
import { Button, Input, SelectControl } from '@backplane-labs/backplane-ui';
import { JSONViewer } from '@backplane-labs/backplane-editors';
import { MutationState, QueryState, usePlatformAction, usePlatformQuery } from './runtime.js';
import { usePlatformText } from './locales.js';
import { nativeJSON } from './serialization.js';

export function ScheduleEditor({ service, name, mode, onSaved }: { service: string; name?: string; mode: 'dark' | 'light'; onSaved: () => void }) {
  const client = useClient(api.ScheduleServiceClient);
  const existing = usePlatformQuery(`schedule:${service}:${name}`, (signal) => name ? client.getSchedule(create(api.GetScheduleRequestSchema, { service, name }), { signal }) : Promise.resolve(null));
  if (existing.loading || existing.error) return <QueryState state={existing} />;
  return <Editor service={service} name={name} existing={existing.value ?? undefined} mode={mode} onSaved={onSaved} />;
}
function Editor({ service, name: originalName, existing, mode, onSaved }: { service: string; name?: string; existing?: api.GetScheduleResponse; mode: 'dark' | 'light'; onSaved: () => void }) {
  const client = useClient(api.ScheduleServiceClient), workflows = useClient(api.WorkflowServiceClient), text = usePlatformText();
  const definitions = usePlatformQuery(`schedule-workflows:${service}`, (signal) => workflows.listWorkflows(create(api.ListWorkflowsRequestSchema, { service }), { signal }));
  const [name, setName] = useState(originalName ?? ''), [workflow, setWorkflow] = useState(existing?.definition?.workflow ?? '');
  const [timing, setTiming] = useState(existing ? 'keep' : 'cron'), [cron, setCron] = useState('0 * * * *'), [seconds, setSeconds] = useState('3600'), [timezone, setTimezone] = useState('UTC'), [paused, setPaused] = useState(false);
  const [input, setInput] = useState(existing?.definition?.input || '{}');
  const action = usePlatformAction<unknown>();
  const selected = definitions.value?.workflows.find((entry) => entry.name === workflow);
  const interval = Number(seconds), validTiming = timing !== 'interval' || Number.isSafeInteger(interval) && interval > 0;
  const disabled = !name.trim() || !selected || !validTiming || (timing === 'cron' && !cron.trim()) || action.pending || action.disabled;
  const save = async (payload: string, signal?: AbortSignal) => {
    const result = await action.run((abort) => {
      const options = { signal: signal ? AbortSignal.any([signal, abort]) : abort };
      const definition = create(api.ScheduleDefinitionSchema, { workflow, input: payload, executionTimeout: existing?.definition?.executionTimeout,
        timing: timing === 'keep' ? undefined : create(api.ScheduleTimingSchema, { timezone, cron: timing === 'cron' ? [cron] : [], interval: timing === 'interval' ? create(DurationSchema, { seconds: BigInt(interval) }) : undefined }) });
      return existing ? client.updateSchedule(create(api.UpdateScheduleRequestSchema, { service, name, definition, revision: existing.revision }), options)
        : client.createSchedule(create(api.CreateScheduleRequestSchema, { service, name, definition, paused }), options);
    });
    if (result === undefined) throw new Error('Schedule outcome is unavailable');
    onSaved();
  };
  return <div className="grid gap-4">
    <label className="grid gap-1 text-sm">{text('scheduleName')}<Input value={name} onChange={(event) => setName(event.target.value)} disabled={!!existing} /></label>
    <label className="grid gap-1 text-sm">{text('workflow')}<SelectControl aria-label={text('workflow')} value={workflow} onValueChange={(value) => { setWorkflow(value); setInput('{}'); }} options={[{ value: '', label: text('selectWorkflow') }, ...(definitions.value?.workflows ?? []).map((entry) => ({ value: entry.name, label: entry.name }))]} /></label>
    <QueryState state={definitions} />
    <label className="grid gap-1 text-sm">{text('scheduleTiming')}<SelectControl aria-label={text('scheduleTiming')} value={timing} onValueChange={setTiming} options={[...(existing ? [{ value: 'keep', label: text('keepTiming') }] : []), { value: 'cron', label: text('cronExpression') }, { value: 'interval', label: text('intervalSeconds') }]} /></label>
    {timing === 'cron' && <label className="grid gap-1 text-sm">{text('cronExpression')}<Input value={cron} onChange={(event) => setCron(event.target.value)} /></label>}
    {timing === 'interval' && <label className="grid gap-1 text-sm">{text('intervalSeconds')}<Input type="number" min={1} value={seconds} aria-invalid={!validTiming} onChange={(event) => setSeconds(event.target.value)} /></label>}
    {timing !== 'keep' && <label className="grid gap-1 text-sm">{text('timezone')}<Input value={timezone} onChange={(event) => setTimezone(event.target.value)} /></label>}
    {existing?.timingJson && <details><summary className="cursor-pointer text-sm">{text('currentTiming')}</summary><JSONViewer label={text('currentTiming')} value={existing.timingJson} mode={mode} /></details>}
    {!existing && <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={paused} onChange={(event) => setPaused(event.target.checked)} />{text('createPaused')}</label>}
    {existing && <p className="text-xs text-muted-foreground">{text('preserveSchedulePause')}</p>}
    {selected?.input ? <SchemaForm key={workflow} schema={selected.input} label={text('input')} initialValues={parse(input, null, parseNumberAndBigInt) as NativeStruct} disabled={disabled} submitLabel={text('saveSchedule')} onSubmit={(baked, signal) => save(nativeJSON(structToNative(baked.values)), signal)} /> : <label className="grid gap-1 text-sm">{text('input')}<textarea className="min-h-32 rounded-md border border-input bg-background p-2 font-mono" value={input} onChange={(event) => setInput(event.target.value)} /><Button disabled={disabled} onClick={() => { void save(input).catch(() => {}); }}>{text('saveSchedule')}</Button></label>}
    <MutationState action={action} />
    {action.error !== undefined && <p className="text-xs text-muted-foreground">{text('scheduleConflictHelp')}</p>}
  </div>;
}
