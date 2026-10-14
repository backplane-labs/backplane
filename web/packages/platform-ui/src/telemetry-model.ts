import { parse, isLosslessNumber, stringify } from 'lossless-json';
import type { ObsRows, ObsTimeSeries, ObsTraceSearch } from '@backplane-labs/backplane-api';
import type { ChartSeries } from '@backplane-labs/backplane-charts';
import type { Attributes, LogRecord, SpanRecord, TraceSummary } from '@backplane-labs/backplane-observability-ui';

export const object = (value: unknown): Record<string, unknown> => value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
const list = (value: unknown): unknown[] => Array.isArray(value) ? value : [];
const scalar = (value: unknown): string => typeof value === 'string' ? value : isLosslessNumber(value) ? value.toString() : typeof value === 'number' || typeof value === 'bigint' ? String(value) : '';
export function parseTelemetryJSON(raw: string): unknown { return parse(raw); }
export function telemetryJSON(value: unknown): string { return stringify(value, null, 2) ?? 'null'; }
export function rowsToLogs(rows: ObsRows, responseId: string): LogRecord[] {
  return rows.rows.map((row, index) => ({ id: `${responseId}:${index}`, timestamp: row.fields._time ?? '', severity: row.fields.severity_text ?? row.fields.SeverityText ?? row.fields.severity,
    body: row.fields._msg ?? '', attributes: row.fields, traceId: row.fields.trace_id ?? row.fields.TraceId, spanId: row.fields.span_id ?? row.fields.SpanId,
    resource: Object.fromEntries(Object.entries(row.fields).filter(([key]) => key.startsWith('service.') || key.startsWith('resource.'))),
  }));
}
export function seriesToCharts(series: ObsTimeSeries): ChartSeries[] {
  return series.series.map((series, index) => ({ id: String(index), label: Object.entries(series.labels).map(([key, value]) => `${key}=${value}`).join(', ') || String(index + 1),
    points: series.samples.map((sample) => ({ x: Number(sample.timestampSeconds) * 1000, value: sample.value })) }));
}
export function traceSearchToList(search: ObsTraceSearch): TraceSummary[] {
  return search.traces.map((trace) => ({ id: trace.traceId, name: trace.rootName, service: trace.rootService, startUnixNano: trace.startUnixNano,
    durationNanos: decimalMillisToNanos(trace.durationMillis),
  }));
}
export function decimalMillisToNanos(value: string): string {
  const match = /^(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/.exec(value);
  if (!match) return '';
  const exponent = Number(match[3] ?? 0), fraction = match[2] ?? '';
  if (!Number.isSafeInteger(exponent) || Math.abs(exponent) > 1000 || value.length > 10000) return '';
  const digits = BigInt(match[1] + fraction), power = 6 + exponent - fraction.length;
  // Sub-nanosecond fractions are truncated; never relabel raw milliseconds as ns.
  return String(power >= 0 ? digits * 10n ** BigInt(power) : digits / 10n ** BigInt(-power));
}
export function otlpId(value: unknown, bytes: 8 | 16): string {
  const source = scalar(value); if (new RegExp(`^[0-9a-fA-F]{${bytes * 2}}$`).test(source)) return source.toLowerCase();
  try { const binary = atob(source); if (binary.length === bytes) return [...binary].map((char) => char.charCodeAt(0).toString(16).padStart(2, '0')).join(''); } catch { /* Retain unrecognized source text. */ }
  return source;
}
function anyValue(input: unknown, depth = 0): unknown {
  if (depth > 32) return telemetryJSON(input);
  const value = object(input);
  if ('stringValue' in value) return value.stringValue;
  if ('boolValue' in value) return value.boolValue;
  if ('intValue' in value) { const number = scalar(value.intValue); return /^-?\d+$/.test(number) ? BigInt(number) : number; }
  if ('doubleValue' in value) return scalar(value.doubleValue);
  if ('bytesValue' in value) return value.bytesValue;
  if ('arrayValue' in value) return list(object(value.arrayValue).values).map((item) => anyValue(item, depth + 1));
  if ('kvlistValue' in value) return attributes(object(value.kvlistValue).values, depth + 1);
  return input;
}
function attributes(input: unknown, depth = 0): Attributes {
  return Object.fromEntries(list(input).map((item) => { const value = object(item); return [scalar(value.key), anyValue(value.value, depth)]; }));
}
/** Tempo v2 contains trace.resourceSpans; unknown extensions stay in the raw viewer. */
export function tempoToSpans(raw: string): SpanRecord[] {
  const envelope = object(parseTelemetryJSON(raw)), trace = object(envelope.trace ?? envelope);
  const result: SpanRecord[] = [];
  for (const group of list(trace.resourceSpans ?? trace.batches)) {
    const resourceSpan = object(group), resource = attributes(object(resourceSpan.resource).attributes);
    for (const scopeGroup of list(resourceSpan.scopeSpans ?? resourceSpan.instrumentationLibrarySpans)) for (const input of list(object(scopeGroup).spans)) {
      const span = object(input), status = object(span.status);
      result.push({ id: otlpId(span.spanId, 8), traceId: otlpId(span.traceId, 16), parentId: span.parentSpanId ? otlpId(span.parentSpanId, 8) : undefined,
        name: scalar(span.name), service: typeof resource['service.name'] === 'string' ? resource['service.name'] : undefined,
        startUnixNano: scalar(span.startTimeUnixNano), endUnixNano: scalar(span.endTimeUnixNano), kind: scalar(span.kind),
        status: ['2', 'STATUS_CODE_ERROR'].includes(scalar(status.code)) ? 'error' : scalar(status.code), attributes: attributes(span.attributes), resource,
        events: list(span.events).map((event) => { const item = object(event); return { name: scalar(item.name), timestamp: scalar(item.timeUnixNano), attributes: attributes(item.attributes) }; }),
        links: list(span.links).map((link) => { const item = object(link); return { traceId: otlpId(item.traceId, 16), spanId: otlpId(item.spanId, 8), attributes: attributes(item.attributes) }; }),
      });
    }
  }
  return result;
}
