import { type Attributes, createTraceState } from '@opentelemetry/api';
import { resourceFromAttributes } from '@opentelemetry/resources';
import type { ReadableLogRecord } from '@opentelemetry/sdk-logs';
import { materialize } from './json.js';

// Explicit DTO: no exporter, Context, credentials, or live application objects.
export function encodeRecord(record: ReadableLogRecord): string {
  if (typeof record.body !== 'string') throw new TypeError('outbox_requires_string_body');
  const scope = record.instrumentationScope;
  const sc = record.spanContext;
  const dto = {
    version: 1,
    hrTime: record.hrTime,
    hrTimeObserved: record.hrTimeObserved,
    ...(record.body === undefined ? {} : { body: record.body }),
    ...(record.eventName === undefined ? {} : { eventName: record.eventName }),
    ...(record.severityText === undefined ? {} : { severityText: record.severityText }),
    ...(record.severityNumber === undefined ? {} : { severityNumber: record.severityNumber }),
    attributes: record.attributes,
    droppedAttributesCount: record.droppedAttributesCount,
    resource: Object.fromEntries(Object.entries(record.resource.attributes).filter(([, value]) => value !== undefined)),
    ...(record.resource.schemaUrl === undefined ? {} : { resourceSchemaUrl: record.resource.schemaUrl }),
    scope: scope.name,
    ...(scope.schemaUrl === undefined ? {} : { scopeSchemaUrl: scope.schemaUrl }),
    ...(scope.attributes === undefined ? {} : { scopeAttributes: scope.attributes }),
    ...(scope.droppedAttributesCount === undefined
      ? {}
      : { scopeDroppedAttributesCount: scope.droppedAttributesCount }),
    ...(record.instrumentationScope.version === undefined ? {} : { scopeVersion: record.instrumentationScope.version }),
    ...(sc
      ? {
          spanContext: {
            traceId: sc.traceId,
            spanId: sc.spanId,
            traceFlags: sc.traceFlags,
            ...(sc.isRemote === undefined ? {} : { isRemote: sc.isRemote }),
            ...(sc.traceState ? { traceState: sc.traceState.serialize() } : {}),
          },
        }
      : {}),
  };
  return JSON.stringify(materialize(dto));
}

export function decodeRecord(payload: string): ReadableLogRecord {
  const value = JSON.parse(payload);
  materialize(value); // Reject non-finite values and retain literal prototype-like keys safely.
  const object = (v: unknown) => v !== null && typeof v === 'object' && !Array.isArray(v);
  const time = (v: unknown) =>
    Array.isArray(v) && v.length === 2 && v.every(Number.isSafeInteger) && v[0] >= 0 && v[1] >= 0 && v[1] < 1e9;
  if (
    !object(value) ||
    value.version !== 1 ||
    !time(value.hrTime) ||
    !time(value.hrTimeObserved) ||
    typeof value.body !== 'string' ||
    typeof value.scope !== 'string' ||
    !object(value.attributes) ||
    !object(value.resource) ||
    !Number.isSafeInteger(value.droppedAttributesCount) ||
    value.droppedAttributesCount < 0 ||
    [value.eventName, value.severityText, value.scopeVersion, value.resourceSchemaUrl, value.scopeSchemaUrl].some(
      (v) => v !== undefined && typeof v !== 'string',
    ) ||
    (value.severityNumber !== undefined &&
      (!Number.isInteger(value.severityNumber) || value.severityNumber < 0 || value.severityNumber > 24)) ||
    (value.scopeAttributes !== undefined && !object(value.scopeAttributes))
  )
    throw new Error('outbox_invalid_record');
  const sc = value.spanContext;
  if (
    sc &&
    (!object(sc) ||
      !/^[0-9a-f]{32}$/.test(sc.traceId) ||
      !/^[0-9a-f]{16}$/.test(sc.spanId) ||
      !Number.isInteger(sc.traceFlags) ||
      sc.traceFlags < 0 ||
      sc.traceFlags > 255 ||
      (sc.traceState !== undefined && typeof sc.traceState !== 'string') ||
      (sc.isRemote !== undefined && typeof sc.isRemote !== 'boolean'))
  )
    throw new Error('outbox_invalid_record');
  return {
    hrTime: value.hrTime,
    hrTimeObserved: value.hrTimeObserved,
    body: value.body,
    eventName: value.eventName,
    severityText: value.severityText,
    severityNumber: value.severityNumber,
    attributes: value.attributes,
    droppedAttributesCount: value.droppedAttributesCount,
    resource: resourceFromAttributes(value.resource as Attributes, {
      schemaUrl: value.resourceSchemaUrl,
    }),
    instrumentationScope: {
      name: value.scope,
      version: value.scopeVersion,
      schemaUrl: value.scopeSchemaUrl,
      attributes: value.scopeAttributes,
      droppedAttributesCount: value.scopeDroppedAttributesCount,
    },
    ...(sc
      ? {
          spanContext: {
            ...sc,
            ...(sc.traceState ? { traceState: createTraceState(sc.traceState) } : {}),
          },
        }
      : {}),
  };
}
