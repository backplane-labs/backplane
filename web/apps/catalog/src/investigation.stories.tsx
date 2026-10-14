import { useState } from 'react';
import type { Meta, StoryObj } from '@storybook/react-vite';
import { useTranslation } from 'react-i18next';
import { Button, DataTable, SearchInput, Stack, Tabs, TabsContent, TabsList, TabsTrigger } from '@backplane-labs/backplane-ui';
import { ErrorDetails, LogViewer, TraceList, TraceWaterfall, type CorrelationTarget, type ErrorCause, type LogRecord, type SpanRecord } from '@backplane-labs/backplane-observability-ui';

const english = { title: 'Error investigation fixture', search: 'Search errors', errors: 'Errors', message: 'Message', service: 'Service', occurrences: 'Occurrences', back: 'Back to errors', details: 'Error detail', stack: 'Stack and causes', logs: 'Related logs', traces: 'Related trace', span: 'Related spans', empty: 'No matching errors' };
const traceId = '0123456789abcdef0123456789abcdef', start = '1790683200123456789';
const error: ErrorCause = { id: 'delivery', type: 'DeliveryError', message: 'Delivery failed for request 42', payload: { request: 42, sequence: 18446744073709551615n }, frames: [{ id: 'send', function: 'deliver', file: 'delivery.ts', line: 12, inApp: true, source: [{ number: 11, text: 'const result = await transport.send(message);' }, { number: 12, text: 'throw new DeliveryError(result.cause);' }] }], cause: { id: 'transport', type: 'TransportError', message: 'Connection refused', frames: [] } };
const rows = [{ id: 'delivery', message: error.message, service: 'hello', occurrences: 18446744073709551615n }, { id: 'timeout', message: 'Request timed out', service: 'formatter', occurrences: 3n }];
const logs: LogRecord[] = [{ id: 'log-42', timestamp: '2026-09-29T12:00:00.123456789Z', body: 'Delivery failed for request 42', severity: 'ERROR', traceId, spanId: 'span-deliver', resource: { 'service.name': 'hello' }, attributes: { sequence: 18446744073709551615n, 'exception.type': 'DeliveryError' } }];
const spans: SpanRecord[] = [{ id: 'span-deliver', traceId, name: 'deliver', service: 'hello', startUnixNano: start, endUnixNano: '1790683200123456888', status: 'error', events: [{ name: 'exception', timestamp: start, attributes: { 'exception.message': error.message } }], attributes: { sequence: 18446744073709551615n } }];
function Investigation({ mode }: { mode: 'dark' | 'light' }) {
  const { t } = useTranslation('fixture.investigation');
  const text = (key: keyof typeof english) => t(key, { defaultValue: english[key] });
  const [search, setSearch] = useState(''), [selected, setSelected] = useState(''), [tab, setTab] = useState('stack');
  const navigate = (target: CorrelationTarget) => setTab(target.signal === 'traces' ? 'traces' : 'logs');
  return <Stack><h1>{text('title')}</h1><SearchInput label={text('search')} value={search} onValueChange={setSearch} />
    {!selected ? <DataTable label={text('errors')} data={rows.filter((row) => row.message.toLowerCase().includes(search.toLowerCase()))} getRowId={(row) => row.id} onActivate={(row) => { setSelected(row.id); setTab('stack'); }} columns={[
      { id: 'message', label: text('message'), value: (row) => row.message, width: 400 }, { id: 'service', label: text('service'), value: (row) => row.service }, { id: 'count', label: text('occurrences'), value: (row) => row.occurrences, width: 240 },
    ]} /> : <><Button variant="outline" onClick={() => setSelected('')}>{text('back')}</Button><h2>{text('details')}</h2>
      <Tabs value={tab} onValueChange={setTab}><TabsList aria-label={text('details')}><TabsTrigger value="stack">{text('stack')}</TabsTrigger><TabsTrigger value="logs">{text('logs')}</TabsTrigger><TabsTrigger value="traces">{text('traces')}</TabsTrigger></TabsList>
        <TabsContent value="stack"><ErrorDetails error={selected === 'delivery' ? error : { id: 'timeout', type: 'Timeout', message: 'Request timed out' }} mode={mode} /></TabsContent>
        <TabsContent value="logs"><LogViewer label={text('logs')} records={selected === 'delivery' ? logs : []} mode={mode} onNavigate={navigate} /></TabsContent>
        <TabsContent value="traces"><TraceList label={text('traces')} traces={selected === 'delivery' ? [{ id: traceId, name: 'deliver', service: 'hello', startUnixNano: start, durationNanos: '99', error: true }] : []} />{selected === 'delivery' && <TraceWaterfall label={text('span')} spans={spans} onNavigate={navigate} />}</TabsContent>
      </Tabs>
    </>}
  </Stack>;
}
export default { title: 'Workflows/Error investigation' } satisfies Meta;
export const SearchToTrace: StoryObj = { render: (_, context) => <Investigation mode={context.globals.theme === 'light' ? 'light' : 'dark'} /> };
