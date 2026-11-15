/**
 * App Debug data contract, draft v0.1 / wire version 1.
 * Declarations only: this file is not an SDK or server implementation.
 * Runtime validation is required for values read from VM.
 */
export type JsonValue = null | boolean | number | string | readonly JsonValue[] | { readonly [key: string]: JsonValue };

/**
 * Any value crossing a capture boundary (a thrown error, a reader's output, a
 * serializer's input). Every JS value is assignable to it, and nothing may be
 * read from it without narrowing: materialize and the exception reader do that
 * once, at the edge.
 */
export type CapturedInput = null | undefined | boolean | number | string | bigint | symbol | object;

/** Decimal UNIX nanoseconds. Browser captures can have millisecond precision. */
export type UnixNano = string;
export type Id = string;

export interface TraceRef {
  readonly traceId: string;
  readonly spanId?: string;
  readonly traceFlags?: number;
  readonly origin: 'active' | 'explicit';
}

export interface CaptureDiagnostic {
  readonly code: string;
  readonly stage: 'read' | 'serialize' | 'validate' | 'capture';
  readonly message?: string;
}

export type CapturedValue =
  | { readonly status: 'ok'; readonly value: JsonValue }
  | { readonly status: 'error'; readonly error: CaptureDiagnostic };

export type SourceSnapshot = CapturedValue & {
  readonly name: string;
  readonly registrationId: Id;
  readonly capturedAtUnixNano: UnixNano;
  readonly monotonicMs: number;
};

export interface HistoryBase {
  readonly id: Id;
  readonly sequence: number;
  readonly timestampUnixNano: UnixNano;
  readonly monotonicMs: number;
  readonly trace?: TraceRef;
}

export type HistoryEntry = HistoryBase &
  (
    | {
        readonly kind: 'breadcrumb';
        readonly name: string;
        readonly data?: CapturedValue;
      }
    | {
        readonly kind: 'state';
        readonly snapshot: SourceSnapshot;
      }
  );

export interface ExceptionInfo {
  readonly cause?: ExceptionInfo;
  readonly errors?: readonly ExceptionInfo[];
  /** Chain could not be read completely; never implies a complete root cause. */
  readonly incomplete?: 'cycle' | 'limit' | 'unreadable';
  readonly type?: string;
  readonly message?: string;
  readonly stacktrace?: string;
}

export interface ExceptionData extends ExceptionInfo {
  readonly type?: string;
  readonly message?: string;
  readonly stacktrace?: string;
  readonly mechanism: 'manual' | 'window.error' | 'unhandledrejection';
  /** Meaning at capture time, not a lifecycle promise. */
  readonly handled?: boolean;
  readonly location?: {
    readonly url?: string;
    readonly line?: number;
    readonly column?: number;
  };
}

/** Client-local diagnostic picture. Not an OTLP envelope or read-server payload. */
export interface DebugSnapshotV1 {
  readonly schema: 'app-debug';
  readonly schemaVersion: 1;
  readonly kind: 'snapshot';
  readonly snapshotId: Id;
  readonly timestampUnixNano: UnixNano;
  readonly monotonicMs: number;
  readonly runtime: { readonly id: Id; readonly sequence: number };
  readonly trace?: TraceRef;
  readonly state: { readonly sources: readonly SourceSnapshot[] };
  readonly history: {
    readonly enabled: boolean;
    readonly sinceUnixNano: UnixNano;
    readonly evictedCount: number;
    /** Items dropped by this snapshot's limits, not normal retention. */
    readonly truncatedCount: number;
    readonly items: readonly HistoryEntry[];
  };
  readonly diagnostics?: readonly CaptureDiagnostic[];
}

export interface DebugEnvelopeV1 {
  readonly schema: 'app-debug';
  readonly schemaVersion: 1;
  readonly kind: 'exception';
  readonly eventId: Id;
  readonly timestampUnixNano: UnixNano;
  readonly monotonicMs: number;
  readonly runtime: {
    readonly id: Id;
    readonly sequence: number;
  };
  readonly exception: ExceptionData;
  readonly trace?: TraceRef;
  readonly state: {
    readonly sources: readonly SourceSnapshot[];
    readonly inline?: CapturedValue;
  };
  readonly history: {
    readonly enabled: boolean;
    readonly sinceUnixNano: UnixNano;
    /** Evictions by age/capacity since the last explicit clear. Not OTLP drops. */
    readonly evictedCount: number;
    readonly items: readonly HistoryEntry[];
  };
  readonly groupKey?: string;
  readonly extensions?: { readonly [namespace: string]: JsonValue };
  /** Capture-local omissions; codes only, no rejected user data. */
  readonly diagnostics?: readonly CaptureDiagnostic[];
}

/** This describes the synchronous emit attempt, NOT durable delivery. */
export type CaptureResult =
  | { readonly status: 'emitted'; readonly eventId: Id }
  | {
      readonly status: 'not_emitted';
      readonly reason: 'closed' | 'reentrant' | 'filtered' | 'encode_failed' | 'emit_failed';
    };

export interface StateSource<T> {
  read(): T;
  serialize(value: T): JsonValue;
}

export interface JsonStateSource {
  read(): JsonValue;
}

export interface TimeRange {
  readonly startUnixNano: UnixNano;
  /** Exclusive. */
  readonly endUnixNano: UnixNano;
}

export interface OccurrenceSearch {
  readonly range: TimeRange;
  readonly service?: string;
  readonly environment?: string;
  readonly exceptionType?: string;
  readonly messageContains?: string;
  readonly traceId?: string;
  readonly runtimeId?: Id;
  readonly groupKey?: string;
  readonly origin?: 'sdk' | 'otel-log' | 'both';
  readonly pageSize?: number;
  readonly cursor?: string;
}

export interface OccurrenceSummary {
  /** Validated, encoded locator, not a database primary key or an auth token. */
  readonly ref: string;
  readonly origin: 'sdk' | 'otel-log';
  readonly eventId?: Id;
  readonly timestampUnixNano: UnixNano;
  readonly service?: string;
  readonly environment?: string;
  readonly release?: string;
  readonly exceptionType?: string;
  readonly message?: string;
  readonly severityNumber?: number;
  readonly traceId?: string;
  readonly spanId?: string;
  readonly runtimeId?: Id;
  readonly contextStatus: 'not_loaded' | 'absent' | 'invalid' | 'unsupported_version';
}

export interface OccurrenceDetail {
  readonly summary: OccurrenceSummary;
  readonly exception:
    | ExceptionData
    | {
        readonly type?: string;
        readonly message?: string;
        readonly stacktrace?: string;
      };
  readonly payload:
    | { readonly status: 'available'; readonly value: DebugEnvelopeV1 }
    | { readonly status: 'absent' | 'invalid' | 'unsupported_version'; readonly raw?: string };
  /** Do not pretend flattened fields are a lossless original OTel Resource. */
  readonly storedFields: Readonly<Record<string, string>>;
  readonly warnings: readonly string[];
}

export interface SearchResponse {
  readonly items: readonly OccurrenceSummary[];
  readonly range: TimeRange;
  readonly nextCursor?: string;
  readonly meta: {
    readonly queryStatus: 'complete' | 'partial';
    readonly servedFrom: 'upstream' | 'cache';
    readonly fetchedAt: string;
    readonly cacheAgeMs?: number;
    readonly warnings: readonly string[];
  };
}

/** Status of an explicit related-data request, not overall error visibility. */
export type RelatedResult<T> =
  | { readonly status: 'available'; readonly data: T }
  | { readonly status: 'partial'; readonly data: T; readonly reason: string }
  | {
      readonly status: 'not_found' | 'unavailable' | 'not_configured';
      readonly reason?: string;
    };

export type RelationEvidence =
  | { readonly kind: 'same_span'; readonly traceId: string; readonly spanId: string }
  | { readonly kind: 'same_trace'; readonly traceId: string }
  | { readonly kind: 'same_runtime'; readonly runtimeId: Id }
  | { readonly kind: 'time_window'; readonly range: TimeRange; readonly service?: string };
