import { ProtobufLogsSerializer } from '@opentelemetry/otlp-transformer';
import type { ReadableLogRecord } from '@opentelemetry/sdk-logs';

// A stored record is an envelope: one version byte, then the record as a
// standard OTLP ExportLogsServiceRequest in protobuf. A reader drops a version
// it does not know without decoding the OTLP; every version a build ever
// wrote stays readable by the later builds.

/** The version this build writes. */
export const recordVersion = 1;

/** The record as stored: the version byte, then the OTLP request. */
export function encodeRecord(record: ReadableLogRecord): Uint8Array {
  const otlp = ProtobufLogsSerializer.serializeRequest([record]);
  if (otlp === undefined) throw new Error('outbox_encode_failed');
  const envelope = new Uint8Array(otlp.byteLength + 1);
  envelope[0] = recordVersion;
  envelope.set(otlp, 1);
  return envelope;
}

export type OpenedRecord =
  | { readonly kind: 'otlp'; readonly request: Uint8Array }
  | { readonly kind: 'unknown_version' }
  | { readonly kind: 'invalid' };

/**
 * The OTLP request of a stored record. A newer version than this build's is
 * unknown_version; an empty envelope or version 0 is invalid.
 */
export function openRecord(envelope: Uint8Array): OpenedRecord {
  const version = envelope[0];
  if (version === undefined || version === 0 || envelope.byteLength < 2) return { kind: 'invalid' };
  if (version > recordVersion) return { kind: 'unknown_version' };
  return { kind: 'otlp', request: envelope.subarray(1) };
}

/**
 * OTLP requests as bodies of at most maxBytes each, in order. Concatenated
 * protobuf messages are one message whose repeated resource_logs merge, so a
 * body is the requests' bytes back to back. A request larger than maxBytes
 * is a body of its own.
 */
export function batchRequests(
  requests: readonly Uint8Array[],
  maxBytes: number,
): { body: Uint8Array; count: number }[] {
  const bodies: { body: Uint8Array; count: number }[] = [];
  let parts: Uint8Array[] = [];
  let size = 0;
  const close = () => {
    if (!parts.length) return;
    const body = new Uint8Array(size);
    let at = 0;
    for (const part of parts) {
      body.set(part, at);
      at += part.byteLength;
    }
    bodies.push({ body, count: parts.length });
    parts = [];
    size = 0;
  };
  for (const request of requests) {
    if (size + request.byteLength > maxBytes) close();
    parts.push(request);
    size += request.byteLength;
  }
  close();
  return bodies;
}
