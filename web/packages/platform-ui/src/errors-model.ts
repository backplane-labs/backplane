import { create, type JsonValue } from '@bufbuild/protobuf';
import { timestampDate, timestampFromMs } from '@bufbuild/protobuf/wkt';
import { ErrorField, ErrorFilterSchema, ErrorOperator, type ErrorFilter, type GetErrorResponse, type RelatedLog } from '@backplane-labs/backplane-api';
import type { ErrorCause, LogRecord, StackFrame } from '@backplane-labs/backplane-observability-ui';
import type { FeedChip, FeedOp } from './feed.js';

/** A filterable field of an error, as chips name it. */
export type ErrorFieldName = 'service' | 'environment' | 'type' | 'message' | 'release' | 'trace_id' | 'runtime_id' | 'group_key' | 'origin';
export const errorFieldNames: readonly ErrorFieldName[] = ['service', 'environment', 'type', 'message', 'release', 'trace_id', 'runtime_id', 'group_key', 'origin'];
export const errorFieldEnum: Record<ErrorFieldName, ErrorField> = {
  service: ErrorField.SERVICE, environment: ErrorField.ENVIRONMENT, type: ErrorField.TYPE, message: ErrorField.MESSAGE, release: ErrorField.RELEASE,
  trace_id: ErrorField.TRACE_ID, runtime_id: ErrorField.RUNTIME_ID, group_key: ErrorField.GROUP_KEY, origin: ErrorField.ORIGIN,
};
const opEnum: Partial<Record<FeedOp, ErrorOperator>> = {
  is: ErrorOperator.IS, is_not: ErrorOperator.IS_NOT, contains: ErrorOperator.CONTAINS, not_contains: ErrorOperator.NOT_CONTAINS,
  prefix: ErrorOperator.PREFIX, exists: ErrorOperator.EXISTS, not_exists: ErrorOperator.NOT_EXISTS,
};
export const isErrorField = (key: string): key is ErrorFieldName => errorFieldNames.includes(key as ErrorFieldName);
export const errorFieldOf = (value: ErrorField): ErrorFieldName => errorFieldNames.find((name) => errorFieldEnum[name] === value) ?? 'service';

/** The request filter of the page's chips, text and resolved range (errors always have one). */
export function errorFilterOf(chips: readonly FeedChip[], text: string, range: { from: number; to: number }): ErrorFilter {
  return create(ErrorFilterSchema, {
    text, start: timestampFromMs(range.from), end: timestampFromMs(range.to),
    conditions: chips.filter((chip) => isErrorField(chip.key) && opEnum[chip.op] !== undefined).map((chip) => ({
      field: errorFieldEnum[chip.key as ErrorFieldName], op: opEnum[chip.op], values: chip.values.map((value) => typeof value === 'string' ? value : JSON.stringify(value)),
    })),
  });
}

const v8 = /^\s*at (?:(.+?) \()?(.+?):(\d+):(\d+)\)?\s*$/;
const gecko = /^\s*(.*?)@(.+?):(\d+):(\d+)\s*$/;
const goFile = /^\s+(\/.+?\.go|[A-Za-z]:\\.+?\.go|\S+\.go):(\d+)(?: \+0x[0-9a-f]+)?\s*$/;

/**
 * Frames of a stack as text: V8 ("at fn (file:1:2)"), Firefox and Safari
 * ("fn@file:1:2"), Go (a function line then "\tfile.go:12 +0x..").
 * Unrecognized lines are skipped; the raw text stays shown next to them.
 */
export function parseStack(stack: string): StackFrame[] {
  const frames: StackFrame[] = [], lines = stack.split('\n');
  for (let at = 0; at < lines.length; at++) {
    const line = lines[at]!;
    const chrome = v8.exec(line);
    if (chrome) { frames.push({ id: String(frames.length), function: chrome[1] || '<anonymous>', file: chrome[2], line: Number(chrome[3]), column: Number(chrome[4]) }); continue; }
    const firefox = gecko.exec(line);
    if (firefox && !line.startsWith(' ')) { frames.push({ id: String(frames.length), function: firefox[1] || '<anonymous>', file: firefox[2], line: Number(firefox[3]), column: Number(firefox[4]) }); continue; }
    const go = goFile.exec(lines[at + 1] ?? '');
    if (go && line && !/^\s/.test(line) && !line.startsWith('goroutine ')) {
      frames.push({ id: String(frames.length), function: line.replace(/\(0x[^)]*\)$|\(\.\.\.\)$|\([^)]*\)$/, '').trim(), file: go[1], line: Number(go[2]) });
      at++;
    }
  }
  return frames;
}

type Json = Record<string, JsonValue>;
const record = (value: JsonValue | undefined): Json | undefined => value && typeof value === 'object' && !Array.isArray(value) ? value : undefined;

/** The cause chain of an error: the SDK envelope's exception (causes, aggregate errors), else the stored exception. */
export function causesOf(detail: GetErrorResponse): ErrorCause {
  const exception = record(detail.exception as JsonValue | undefined);
  const fromEnvelope = (value: Json | undefined, id: string, depth: number): ErrorCause | undefined => {
    if (!value || depth > 16) return undefined;
    const stack = typeof value.stacktrace === 'string' ? value.stacktrace : '';
    const errors = Array.isArray(value.errors) ? value.errors.map(record).filter((entry): entry is Json => !!entry) : [];
    return {
      id, type: typeof value.type === 'string' ? value.type : undefined, message: typeof value.message === 'string' ? value.message : '',
      frames: parseStack(stack), cause: fromEnvelope(record(value.cause), `${id}.cause`, depth + 1),
      payload: errors.length ? { errors } : undefined,
    };
  };
  return fromEnvelope(exception, 'exception', 0) ?? {
    id: 'exception', type: detail.occurrence?.type, message: detail.occurrence?.message ?? '', frames: parseStack(detail.stacktrace),
  };
}

/** Related logs as the log viewer shows them. */
export function relatedRecords(logs: readonly RelatedLog[]): LogRecord[] {
  return logs.map((log, index) => ({
    id: `related:${index}`, timestamp: log.time ? timestampDate(log.time).toISOString() : '', severity: log.fields.severity_text ?? log.fields.severity,
    body: log.fields._msg ?? '', attributes: log.fields, traceId: log.fields.trace_id, spanId: log.fields.span_id,
    resource: Object.fromEntries(Object.entries(log.fields).filter(([key]) => key.startsWith('service.') || key.startsWith('deployment.'))),
  }));
}

/** An entry of the SDK's history: a breadcrumb or a state snapshot. */
export interface HistoryItem { sequence: number; time?: Date; kind: string; name: string; data?: JsonValue }

/** The envelope's history, oldest first. */
export function historyOf(envelope: JsonValue | undefined): HistoryItem[] {
  const items = record(record(envelope)?.history)?.items;
  if (!Array.isArray(items)) return [];
  return items.map(record).filter((item): item is Json => !!item).map((item) => {
    const snapshot = record(item.snapshot);
    const nanos = typeof item.timestampUnixNano === 'string' ? Number(BigInt(item.timestampUnixNano) / 1_000_000n) : undefined;
    return {
      sequence: typeof item.sequence === 'number' ? item.sequence : 0, time: nanos === undefined ? undefined : new Date(nanos), kind: String(item.kind ?? ''),
      name: String(item.name ?? snapshot?.name ?? ''), data: item.kind === 'state' ? snapshot?.value : record(item.data)?.value ?? item.data,
    };
  }).sort((a, b) => a.sequence - b.sequence);
}

/** The envelope's state: each registered source's value and the inline state. */
export function stateOf(envelope: JsonValue | undefined): { name: string; value: JsonValue }[] {
  const state = record(record(envelope)?.state);
  const sources = Array.isArray(state?.sources) ? state.sources.map(record).filter((source): source is Json => !!source) : [];
  const out = sources.map((source) => ({ name: String(source.name ?? ''), value: source.value ?? source.status ?? null }));
  const inline = record(state?.inline);
  if (inline?.value !== undefined) out.push({ name: 'inline', value: inline.value });
  return out;
}
