import { useMemo } from 'react';
import { Button, DataTable, type DataColumn } from '@backplane-labs/backplane-ui';
import { displayJSON } from '@backplane-labs/backplane-editors';
import { useObsText } from './locales.js';
import type { Attributes, CorrelationTarget } from './types.js';
export function attributeText(value: unknown) { return typeof value === 'string' ? value : typeof value === 'bigint' ? String(value) : displayJSON(value); }
export function AttributeTable({ label, attributes, onFilter }: { label: string; attributes: Attributes; onFilter?: (name: string, value: unknown) => void }) {
  const text = useObsText(), rows = useMemo(() => Object.entries(attributes).map(([key, value]) => ({ key, value })), [attributes]);
  const columns: DataColumn<typeof rows[number]>[] = [{ id: 'key', label: text('name'), value: (row) => row.key, width: 260 },
    { id: 'value', label: text('value'), value: (row) => attributeText(row.value), width: 480, render: (row) => <span title={attributeText(row.value)}>{attributeText(row.value)}</span> }];
  return <DataTable label={label} data={rows} columns={columns} getRowId={(row) => row.key} height={220} onActivate={onFilter ? (row) => onFilter(row.key, row.value) : undefined} />;
}
export function CorrelationLinks({ target, onNavigate }: { target: Omit<CorrelationTarget, 'signal'>; onNavigate: (target: CorrelationTarget) => void }) {
  const text = useObsText();
  return <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
    <Button type="button" variant="outline" onClick={() => onNavigate({ ...target, signal: 'logs' })}>{text('logs')}</Button>
    <Button type="button" variant="outline" onClick={() => onNavigate({ ...target, signal: 'metrics' })}>{text('metrics')}</Button>
    {target.traceId && <Button type="button" variant="outline" onClick={() => onNavigate({ ...target, signal: 'traces' })}>{text('traces')}</Button>}
  </div>;
}
export function HighlightText({ value, search }: { value: string; search: string }) {
  if (!search) return <>{value}</>;
  const lower = value.toLowerCase(), needle = search.toLowerCase(); const parts = []; let start = 0, at: number, count = 0;
  while ((at = lower.indexOf(needle, start)) >= 0 && count++ < 1000) {
    parts.push(value.slice(start, at), <mark key={at} style={{ background: 'var(--warning)', color: 'var(--warning-foreground)' }}>{value.slice(at, at + search.length)}</mark>); start = at + search.length;
  }
  parts.push(value.slice(start)); return <>{parts}</>;
}
