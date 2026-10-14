import { useState } from 'react';
import { DataTable } from '@backplane-labs/backplane-ui';
import { JSONViewer } from '@backplane-labs/backplane-editors';
import { useObsText } from './locales.js';
import type { ErrorCause, SourceLine, StackFrame } from './types.js';
export function SourceContext({ lines, line, label }: { lines: readonly SourceLine[]; line?: number; label: string }) {
  const at = lines.findIndex((item) => item.number === line), visible = lines.slice(Math.max(0, at - 10), Math.max(0, at - 10) + 21);
  return <pre aria-label={label} style={{ overflow: 'auto', fontFamily: 'var(--font-mono)', padding: 8, background: 'var(--muted)' }}>{visible.map((item) => <div key={item.number} style={{ background: item.number === line ? 'var(--accent)' : undefined }}><span aria-hidden="true">{item.number === line ? '→ ' : '  '}</span>{item.number}: {item.text}</div>)}</pre>;
}
export function StackTrace({ frames, label }: { frames: readonly StackFrame[]; label: string }) {
  const text = useObsText(), [selected, setSelected] = useState<string>(); const frame = frames.find((frame) => frame.id === selected);
  return <section aria-label={label}><DataTable label={label} data={[...frames]} getRowId={(frame) => frame.id} onActivate={(frame) => setSelected(frame.id)} height={260} columns={[
    { id: 'function', label: text('function'), value: (frame) => frame.function, width: 280 }, { id: 'file', label: text('file'), value: (frame) => frame.file, width: 320 },
    { id: 'line', label: text('line'), value: (frame) => frame.line, render: (frame) => `${frame.line ?? ''}${frame.column === undefined ? '' : `:${frame.column}`}` },
    { id: 'inApp', label: text('inApp'), value: (frame) => frame.inApp ?? false },
  ]} />{frame?.source && <SourceContext label={text('source')} lines={frame.source} line={frame.line} />}</section>;
}
export function ErrorDetails({ error, mode }: { error: ErrorCause; mode: 'dark' | 'light' }) {
  const text = useObsText(); const causes: ErrorCause[] = [], seen = new Set<ErrorCause>(); let current: ErrorCause | undefined = error;
  while (current && !seen.has(current) && causes.length < 50) { seen.add(current); causes.push(current); current = current.cause; }
  return <section aria-label={text('causes')} style={{ display: 'grid', gap: 16 }}>{causes.map((cause, index) => <article key={`${cause.id}:${index}`}>
    <h3>{cause.type}</h3><p style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{cause.message}</p>
    {!!cause.frames?.length && <StackTrace label={cause.type ?? cause.id} frames={cause.frames} />}
    {cause.payload !== undefined && <JSONViewer label={text('payload')} value={cause.payload} mode={mode} />}
  </article>)}{current && <p role="status">{text('truncatedCauses')}</p>}</section>;
}
