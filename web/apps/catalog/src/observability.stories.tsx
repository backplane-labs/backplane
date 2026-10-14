import { useState } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { ErrorDetails, LogViewer, TraceList, TraceWaterfall, type CorrelationTarget, type ErrorCause, type LogRecord, type SpanRecord } from '@backplane-labs/backplane-observability-ui';
import { Button, Stack } from '@backplane-labs/backplane-ui';
const traceId = '0123456789abcdef0123456789abcdef';
const logs: LogRecord[] = Array.from({ length: 10000 }, (_, index) => ({ id: `log-${index}`, timestamp: `2026-09-29T12:00:00.${String(index).padStart(9, '0')}Z`, severity: index % 3 ? 'info' : 'error', body: `Request ${index} failed <script>unsafe()</script>`, attributes: { sequence: 18446744073709551615n, retry: index }, resource: { 'service.name': index % 2 ? 'hello' : 'formatter' }, traceId, spanId: String(index) }));
const base = 1790683200123456789n;
const spans: SpanRecord[] = Array.from({ length: 2000 }, (_, index) => ({ id: `span-${index}`, parentId: index ? 'span-0' : undefined, traceId, name: index ? `Format ${index}` : 'Greet', service: index ? 'formatter' : 'hello', startUnixNano: String(base + BigInt(index) * 1000n), endUnixNano: String(base + BigInt(index) * 1000n + 999n), status: index % 2 ? 'error' : 'ok', attributes: { sequence: 18446744073709551615n }, events: [{ name: 'exception', timestamp: String(base + 100n), attributes: { 'exception.message': 'failed' } }], links: [{ traceId: 'fedcba9876543210fedcba9876543210', spanId: 'other', attributes: { reason: 'queue' } }] }));
spans[0].endUnixNano = String(base + 3000000n);
function Logs({ mode }: { mode: 'dark' | 'light' }) {
  const [reverse, setReverse] = useState(false), [target, setTarget] = useState<CorrelationTarget>(), [context, setContext] = useState('');
  return <Stack><h1>Virtual logs</h1><Button onClick={() => setReverse(!reverse)}>Reverse logs</Button><LogViewer label="Service logs" records={reverse ? [...logs].reverse() : logs} mode={mode} partial onNavigate={setTarget} onContext={(record) => setContext(record.id)} />
    <output aria-label="Correlation target">{JSON.stringify(target)}</output><output aria-label="Context log">{context}</output></Stack>;
}
function Traces() {
  const [target, setTarget] = useState<CorrelationTarget>();
  return <Stack><h1>Trace investigation</h1><TraceList label="Trace results" traces={[{ id: traceId, name: 'Greet', service: 'hello', startUnixNano: String(base), durationNanos: '3000000', error: true }]} />
    <TraceWaterfall label="Trace spans" spans={spans} onNavigate={setTarget} partial /><output aria-label="Correlation target">{JSON.stringify(target)}</output>
    <TraceWaterfall label="Malformed trace" spans={[{ ...spans[0], id: 'a', parentId: 'b' }, { ...spans[1], id: 'b', parentId: 'a' }]} />
  </Stack>;
}
export const failure: ErrorCause = { id: 'root', type: 'DeliveryError', message: 'Cannot deliver notification', frames: [{ id: 'send', function: 'deliver', file: 'service.ts', line: 12, column: 4, inApp: true, source: Array.from({ length: 100 }, (_, index) => ({ number: index + 1, text: index === 11 ? 'throw new DeliveryError(cause);' : '// source context' })) }], payload: { sequence: 18446744073709551615n }, cause: { id: 'db', type: 'DatabaseError', message: 'Connection refused', frames: [{ id: 'connect', function: 'connect', file: 'driver.ts', line: 42, inApp: false }] } };
export default { title: 'Kit/Observability' } satisfies Meta;
type Story = StoryObj;
export const LogsAndContext: Story = { render: (_, context) => <Logs mode={context.globals.theme === 'light' ? 'light' : 'dark'} /> };
export const TracesAndLinks: Story = { render: () => <Traces /> };
export const ErrorAndCauses: Story = { render: (_, context) => <Stack><h1>Error presentation</h1><ErrorDetails error={failure} mode={context.globals.theme === 'light' ? 'light' : 'dark'} /></Stack> };
