import { useMemo, useState } from 'react';
import { Button, DataTable, Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, type DataColumn } from '@backplane-labs/backplane-ui';
import { AttributeTable, CorrelationLinks } from './attributes.js';
import { duration, nanoDate, nanos, spanForest, spanPosition, type SpanNode } from './model.js';
import { useObsText } from './locales.js';
import type { CorrelationTarget, SpanRecord, TraceSummary } from './types.js';

export function TraceList({ label, traces, timeZone = 'UTC', onSelect, partial }: { label: string; traces: readonly TraceSummary[]; timeZone?: string; onSelect?: (trace: TraceSummary) => void; partial?: boolean }) {
  const text = useObsText();
  return <DataTable label={label} data={[...traces]} getRowId={(trace) => trace.id} onActivate={onSelect} partial={partial} columns={[
    { id: 'name', label: text('name'), value: (trace) => trace.name, width: 280 }, { id: 'service', label: text('service'), value: (trace) => trace.service },
    { id: 'time', label: text('time'), value: (trace) => nanos(trace.startUnixNano), render: (trace) => <span title={trace.startUnixNano}>{nanoDate(trace.startUnixNano, timeZone)}</span>, width: 360 },
    { id: 'duration', label: text('duration'), value: (trace) => nanos(trace.durationNanos), render: (trace) => `${trace.durationNanos} ns` },
    { id: 'status', label: text('status'), value: (trace) => trace.error ? 'error' : '' },
  ]} />;
}
export function SpanDetails({ span, onNavigate }: { span: SpanRecord; onNavigate?: (target: CorrelationTarget) => void }) {
  const text = useObsText();
  return <section aria-label={text('spanDetails')} style={{ display: 'grid', gap: 12 }}><dl>
    <dt>{text('trace')}</dt><dd>{span.traceId}</dd><dt>{text('span')}</dt><dd>{span.id}</dd><dt>{text('start')}</dt><dd>{span.startUnixNano}</dd>
    <dt>{text('end')}</dt><dd>{span.endUnixNano}</dd><dt>{text('duration')}</dt><dd>{duration(span.startUnixNano, span.endUnixNano)}</dd>
  </dl><AttributeTable label={text('attributes')} attributes={span.attributes ?? {}} /><AttributeTable label={text('resource')} attributes={span.resource ?? {}} />
    <section aria-label={text('events')}><h3>{text('events')}</h3>{span.events?.map((event, index) => <details key={index}><summary>{event.timestamp} · {event.name}</summary><AttributeTable label={event.name} attributes={event.attributes ?? {}} /></details>)}</section>
    <section aria-label={text('links')}><h3>{text('links')}</h3>{span.links?.map((link, index) => <details key={index}><summary>{link.traceId} · {link.spanId}</summary><AttributeTable label={link.spanId} attributes={link.attributes ?? {}} />{onNavigate && <CorrelationLinks target={link} onNavigate={onNavigate} />}</details>)}</section>
    {onNavigate && <CorrelationLinks target={{ traceId: span.traceId, spanId: span.id, resource: span.resource, timestamp: span.startUnixNano }} onNavigate={onNavigate} />}
  </section>;
}
export function TraceWaterfall({ label, spans, partial, onNavigate }: { label: string; spans: readonly SpanRecord[]; partial?: boolean; onNavigate?: (target: CorrelationTarget) => void }) {
  const text = useObsText(), forest = useMemo(() => spanForest(spans), [spans]), [selected, setSelected] = useState<string>();
  const span = spans.find((span) => span.id === selected);
  const columns: DataColumn<SpanNode>[] = [
    { id: 'name', label: text('span'), value: (span) => span.name, width: 300, render: (span) => <span style={{ paddingLeft: Math.min(span.depth, 12) * 8 }}>{span.name}</span> },
    { id: 'service', label: text('service'), value: (span) => span.service },
    { id: 'duration', label: text('duration'), value: (span) => duration(span.startUnixNano, span.endUnixNano) },
    { id: 'status', label: text('status'), value: (span) => span.status },
    { id: 'timeline', label: text('timeline'), value: (span) => span.startUnixNano, width: 360, render: (span) => {
      const { left, width } = spanPosition(span, forest.start, forest.end);
      return <div title={duration(span.startUnixNano, span.endUnixNano)} style={{ height: 16, position: 'relative', borderBottom: '1px solid var(--border)' }}><div style={{ position: 'absolute', left: `${left}%`, width: `${width}%`, minWidth: 1, height: 8, top: 4, background: span.status === 'error' ? 'var(--destructive)' : 'var(--primary)' }} /></div>;
    } },
    { id: 'details', label: text('details'), value: (span) => span.id, render: (span) => <Button type="button" variant="ghost" onClick={() => setSelected(span.id)}>{text('details')}</Button>, sortable: false, filterable: false },
  ];
  return <section aria-label={label}>{forest.malformed && <p role="status">{text('malformed')}</p>}{forest.invalidTime && <p role="status">{text('invalidTime')}</p>}
    <DataTable label={label} data={forest.roots} getRowId={(span) => span.id} getChildren={(span) => span.children} columns={columns} partial={partial} onActivate={(span) => setSelected(span.id)} />
    <Dialog open={!!span} onOpenChange={(open) => { if (!open) setSelected(undefined); }}><DialogContent style={{ maxWidth: 'min(1000px, 95vw)', maxHeight: '90vh', overflow: 'auto' }}>
      <DialogHeader><DialogTitle>{span?.name}</DialogTitle><DialogDescription>{text('spanDetails')}</DialogDescription></DialogHeader>
      {span && <SpanDetails span={span} onNavigate={onNavigate} />}
    </DialogContent></Dialog>
  </section>;
}
