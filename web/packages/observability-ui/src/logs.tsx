import { useMemo, useState } from 'react';
import { Button, DataTable, Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, Input, type DataColumn } from '@backplane-labs/backplane-ui';
import { JSONViewer } from '@backplane-labs/backplane-editors';
import { AttributeTable, CorrelationLinks, HighlightText, attributeText } from './attributes.js';
import { useObsText } from './locales.js';
import type { CorrelationTarget, LogRecord } from './types.js';

export function LogViewer({ label, records, mode, loading, error, partial, onContext, onNavigate }: {
  label: string; records: readonly LogRecord[]; mode: 'dark' | 'light'; loading?: boolean; error?: string; partial?: boolean;
  onContext?: (record: LogRecord) => void; onNavigate?: (target: CorrelationTarget) => void;
}) {
  const text = useObsText(), [search, setSearch] = useState(''), [selected, setSelected] = useState<string>();
  const record = records.find((record) => record.id === selected), data = useMemo(() => [...records], [records]);
  const columns: DataColumn<LogRecord>[] = [
    { id: 'time', label: text('time'), value: (record) => record.timestamp, width: 280 },
    { id: 'severity', label: text('severity'), value: (record) => record.severity ?? '', width: 120 },
    { id: 'body', label: text('body'), value: (record) => attributeText(record.body), width: 520, render: (record) => <HighlightText value={attributeText(record.body)} search={search} /> },
    { id: 'trace', label: text('trace'), value: (record) => record.traceId ?? '', width: 280 },
    { id: 'details', label: text('details'), value: (record) => record.id, sortable: false, filterable: false, render: (record) => <Button type="button" variant="ghost" onClick={() => setSelected(record.id)}>{text('details')}</Button> },
  ];
  return <section aria-label={label}><Input aria-label={text('search')} value={search} onChange={(event) => setSearch(event.target.value)} />
    <DataTable label={label} data={data} columns={columns} getRowId={(record) => record.id} selectable loading={loading} error={error} partial={partial} onActivate={(record) => setSelected(record.id)} />
    <Dialog open={!!record} onOpenChange={(open) => { if (!open) setSelected(undefined); }}><DialogContent style={{ maxWidth: 'min(1000px, 95vw)', maxHeight: '90vh', overflow: 'auto' }}>
      <DialogHeader><DialogTitle>{text('logDetails')}</DialogTitle><DialogDescription>{record?.timestamp}</DialogDescription></DialogHeader>
      {record && <><JSONViewer label={text('body')} value={typeof record.body === 'string' ? JSON.stringify(record.body) : record.body} mode={mode} />
        <AttributeTable label={text('attributes')} attributes={record.attributes ?? {}} /><AttributeTable label={text('resource')} attributes={record.resource ?? {}} />
        {onContext && <Button type="button" variant="outline" onClick={() => onContext(record)}>{text('context')}</Button>}
        {onNavigate && <CorrelationLinks target={{ traceId: record.traceId, spanId: record.spanId, resource: record.resource, timestamp: record.timestamp }} onNavigate={onNavigate} />}
      </>}
    </DialogContent></Dialog>
  </section>;
}
