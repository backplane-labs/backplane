import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { toNative, type Native, type NativeStruct, type Schema, type Schema_Field } from '@gopherex/schemapb';
import { Button, DataTable, Timestamp, type DataColumn } from '@backplane-labs/backplane-ui';
import { encodeValue, scalarText } from './model.js';
import { schemaFormsEnglish } from './locales.js';

function useText() {
  const { t } = useTranslation('backplane.schemaForms');
  return (key: keyof typeof schemaFormsEnglish, args?: Record<string, string | number>) => t(key, { defaultValue: schemaFormsEnglish[key], ...args });
}

type Sortable = string | number | bigint | boolean | null | undefined;

/**
 * A column of a SchemaTable that is not a field of the item schema as is:
 * derived from the row, or a field shown another way.
 */
export interface SchemaTableColumn {
  /** A field name of the item schema, or the derived column's own id. */
  id: string;
  /** Header; a field's title or name by default. */
  label?: string;
  /** What the column sorts and filters by; the field's value by default. */
  value?: (row: NativeStruct) => Sortable;
  /** The cell; the field's formatted value by default. */
  render?: (row: NativeStruct) => ReactNode;
  width?: number;
}

/** Cursor pagination of a SchemaTable: the rows so far and how to get more. */
export interface SchemaTableMore {
  hasMore: boolean;
  loading?: boolean;
  onMore: () => void;
}

export interface SchemaTableProps {
  /** The schema of one row (the list's item message). */
  schema: Schema;
  /** Native schemapb values, one per row. */
  rows: readonly NativeStruct[];
  label: string;
  /**
   * The columns and their order: field names of the schema and derived
   * columns. By default every field that has a cell of its own (scalars,
   * choices, bytes, timestamps, durations), in schema order. The viewer can
   * still hide and reorder them.
   */
  columns?: readonly (string | SchemaTableColumn)[];
  /** Row identity; the `id` field when the schema has one, else the index. */
  rowId?: (row: NativeStruct, index: number) => string;
  /** Actions of a row, in a last column. */
  actions?: (row: NativeStruct) => ReactNode;
  onActivate?: (row: NativeStruct) => void;
  selectable?: boolean;
  onSelectionChange?: (ids: string[]) => void;
  height?: number;
  loading?: boolean;
  error?: string;
  more?: SchemaTableMore;
}

/** A table of rows described by a schemapb Schema, over DataTable. */
export function SchemaTable({ schema, rows, label, columns, rowId, actions, onActivate, selectable, onSelectionChange, height, loading, error, more }: SchemaTableProps) {
  const t = useText();
  const fields = useMemo(() => new Map(schema.fields.map((field) => [field.name, field])), [schema]);
  const definitions = useMemo<DataColumn<Indexed>[]>(() => {
    const chosen = columns ?? schema.fields.filter(ownCell).map((field) => field.name);
    const out = chosen.map((entry): DataColumn<Indexed> => {
      const column: SchemaTableColumn = typeof entry === 'string' ? { id: entry } : entry;
      const field = fields.get(column.id);
      const value = column.value ?? ((row: NativeStruct) => field ? sortable(field, row[field.name]) : undefined);
      return {
        id: column.id, label: column.label ?? (field ? field.title || field.name : column.id), width: column.width,
        value: (row) => value(row.values),
        render: (row) => column.render ? column.render(row.values) : field ? <Cell field={field} value={row.values[field.name]} yes={t('yes')} no={t('no')} /> : String(value(row.values) ?? ''),
      };
    });
    if (actions) out.push({ id: '__actions', label: t('actions'), value: () => undefined, render: (row) => actions(row.values), sortable: false, filterable: false });
    return out;
  }, [actions, columns, fields, schema, t]);
  const indexed = useMemo(() => rows.map((values, index): Indexed => ({ values, index })), [rows]);
  const id = useCallback((row: Indexed) => {
    if (rowId) return rowId(row.values, row.index);
    const own = fields.has('id') ? row.values.id : undefined;
    return own === undefined || own === null ? String(row.index) : scalarText(own);
  }, [fields, rowId]);
  return <div className="flex min-w-0 flex-col gap-2">
    <DataTable label={label} data={indexed} columns={definitions} getRowId={id} height={height} loading={loading} error={error}
      selectable={selectable} onSelectionChange={onSelectionChange} onActivate={onActivate ? (row) => onActivate(row.values) : undefined} />
    {more && (more.hasMore || more.loading) && <Button type="button" variant="outline" size="sm" className="self-start" disabled={!more.hasMore || more.loading} onClick={more.onMore}>
      {t(more.loading ? 'loadingMore' : 'loadMore')}</Button>}
  </div>;
}

interface Indexed { values: NativeStruct; index: number }

/** Fields with a cell of their own; structured ones are shown by a derived column. */
function ownCell(field: Schema_Field): boolean {
  switch (field.kind.case) {
    case 'object': case 'ref': case 'list': case 'map': case 'json': case 'oneOf': case 'computed': return false;
    default: return true;
  }
}

function sortable(field: Schema_Field, value: Native | undefined): Sortable {
  if (value === undefined || value === null) return null;
  if (field.secret) return null;
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'bigint' || typeof value === 'boolean') return value;
  if (value instanceof Uint8Array) return bytesText(value);
  return scalarText(value);
}

/** One value as a table shows it: a choice by its label, bytes as hex or a uuid, a timestamp as a date. */
export function Cell({ field, value, yes = 'Yes', no = 'No' }: { field: Schema_Field; value: Native | undefined; yes?: string; no?: string }) {
  if (value === undefined || value === null) return <span className="text-muted-foreground">—</span>;
  if (field.secret) return <span title="hidden">••••</span>;
  switch (field.kind.case) {
    case 'bool': return <>{value === true ? yes : no}</>;
    case 'choice': {
      const option = field.kind.value.options.find((candidate) => same(toNative(candidate.value), value));
      return <>{option?.label || scalarText(value)}</>;
    }
    case 'bytes': return <span className="font-mono">{value instanceof Uint8Array ? bytesText(value) : scalarText(value)}</span>;
    case 'timestamp': {
      const text = scalarText(value); const date = new Date(text);
      return Number.isNaN(date.getTime()) ? <>{text}</> : <Timestamp value={date} absolute />;
    }
    case 'object': case 'ref': case 'list': case 'map': case 'json': case 'oneOf': {
      const text = encodeValue(value);
      return <span className="font-mono text-xs" title={text}>{text.replace(/\s+/g, ' ')}</span>;
    }
    default: return <>{scalarText(value)}</>;
  }
}

function same(a: Native | undefined, b: Native): boolean {
  return a === b || (typeof a === 'bigint' || typeof b === 'bigint' ? String(a) === String(b) : false);
}

/** 16 bytes read as a uuid, anything else as hex. */
export function bytesText(bytes: Uint8Array): string {
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
  return bytes.length === 16 ? `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}` : hex;
}

/** A page of a cursor-paginated list: its items and where the next starts. */
export interface CursorPage<T> {
  items: readonly T[];
  /** The next page's cursor; empty or absent at the end. */
  nextCursor?: string;
  /** Whether a next page exists; by default whether nextCursor is set. */
  hasMore?: boolean;
}

/**
 * Pages of a cursor-paginated method (next_cursor / has_more), appended:
 * load runs for the first page now and for each next one on more(). A
 * change of deps starts over; a page in flight is cancelled.
 */
export function useCursorPages<T>(load: (cursor: string | undefined, signal: AbortSignal) => Promise<CursorPage<T>>, deps: readonly unknown[]) {
  const [rows, setRows] = useState<T[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string>();
  const current = useRef<AbortController>(undefined);
  const loader = useRef(load);
  loader.current = load;

  const fetchPage = useCallback(async (from: string | undefined, append: boolean) => {
    current.current?.abort();
    const abort = new AbortController(); current.current = abort;
    setLoading(true); setError(undefined);
    try {
      const page = await loader.current(from, abort.signal);
      if (abort.signal.aborted) return;
      setRows((prev) => append ? [...prev, ...page.items] : [...page.items]);
      setCursor(page.nextCursor || undefined);
      setHasMore(page.hasMore ?? !!page.nextCursor);
    } catch (cause) {
      if (!abort.signal.aborted) setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (current.current === abort) setLoading(false);
    }
  }, []);

  useEffect(() => { setRows([]); void fetchPage(undefined, false); return () => current.current?.abort(); }, deps);

  return {
    rows, hasMore, loading, error,
    more: () => { if (hasMore && !loading) void fetchPage(cursor, true); },
    reload: () => { void fetchPage(undefined, false); },
  };
}
