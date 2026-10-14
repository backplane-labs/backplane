import { useEffect, useState } from 'react';
import { fromJsonString, toJsonString, type DescMessage, type MessageShape } from '@bufbuild/protobuf';
import { Button, Input } from '@backplane-labs/backplane-ui';
import { CodeEditor, DiffViewer } from '@backplane-labs/backplane-editors';
import { MutationState, usePlatformAction } from './runtime.js';
import { usePlatformText } from './locales.js';

export function DefinitionEditor<S extends DescMessage>({ schema, initialValue, mode, validate, save }: {
  schema: S; initialValue: MessageShape<S>; mode: 'dark' | 'light';
  validate: (definition: MessageShape<S>, signal: AbortSignal) => Promise<readonly { path: string; message: string }[]>;
  save: (definition: MessageShape<S>, comment: string, signal: AbortSignal) => Promise<{ violations: readonly { path: string; message: string }[]; version?: bigint }>;
}) {
  const text = usePlatformText(), [baseline, setBaseline] = useState(toJsonString(schema, initialValue, { prettySpaces: 2 }));
  const [value, setValue] = useState(baseline), [comment, setComment] = useState(''), [violations, setViolations] = useState<readonly { path: string; message: string }[]>([]), [valid, setValid] = useState(false);
  const action = usePlatformAction<bigint | undefined>(); const dirty = value !== baseline;
  useEffect(() => { if (!dirty) return; const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; }; window.addEventListener('beforeunload', warn); return () => window.removeEventListener('beforeunload', warn); }, [dirty]);
  return <div style={{ display: 'grid', gap: 12 }}>{dirty && <p role="status">{text('dirty')}</p>}
    <CodeEditor label={text('definition')} value={value} onChange={(value) => { setValue(value); setValid(false); setViolations([]); }} language="json" mode={mode} disabled={action.pending} height={360} />
    <Input aria-label={text('comment')} value={comment} onChange={(event) => setComment(event.target.value)} disabled={action.pending} />
    <div style={{ display: 'flex', gap: 8 }}><Button type="button" variant="outline" disabled={action.pending || action.disabled} onClick={() => void action.run(async (signal) => {
      const violations = await validate(fromJsonString(schema, value), signal); if (!signal.aborted) { setViolations(violations); setValid(!violations.length); } return undefined;
    })}>{text('validate')}</Button><Button type="button" disabled={action.pending || action.disabled} onClick={() => void action.run(async (signal) => {
      const response = await save(fromJsonString(schema, value), comment, signal);
      if (!signal.aborted) { setViolations(response.violations); if (response.version !== undefined) setBaseline(value); } return response.version;
    })}>{text('save')}</Button><Button type="button" variant="outline" disabled={action.pending || !dirty} onClick={() => setValue(baseline)}>{text('reset')}</Button></div>
    <MutationState action={action} />{valid && <p role="status">{text('valid')}</p>}{action.value !== undefined && <p role="status">{text('saved')}: {String(action.value)}</p>}
    {!!violations.length && <ul role="alert">{violations.map((item, index) => <li key={index}>{item.path}: {item.message}</li>)}</ul>}
    {dirty && <DiffViewer label={text('definition')} before={baseline} after={value} mode={mode} language="json" />}
  </div>;
}
