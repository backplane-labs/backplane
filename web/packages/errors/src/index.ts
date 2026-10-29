import { context, trace, isSpanContextValid, ROOT_CONTEXT, type Context } from '@opentelemetry/api'
import { SeverityNumber, type LoggerProvider, type LogAttributes } from '@opentelemetry/api-logs'
import { materialize } from './json.js'
import { budgetSnapshot } from './snapshot.js'
import { SDK_VERSION } from './version.js'
import { globalScope } from './scope.js'
export { SDK_VERSION } from './version.js'
import { exceptionReader, type ExceptionLimits } from './exception.js'
import {
  tokenBucket,
  type Sanitizer,
  type DataArea,
  type RateLimit,
  type CaptureFilter,
  type ClientStats,
} from './policy.js'
export { redactKeys } from './policy.js'
export type { Sanitizer, SanitizeContext, ClientStats, RateLimit } from './policy.js'
export type { ExceptionLimits } from './exception.js'
import type {
  CaptureDiagnostic,
  CapturedValue,
  CaptureResult,
  DebugEnvelopeV1,
  DebugSnapshotV1,
  ExceptionData,
  HistoryEntry,
  JsonStateSource,
  JsonValue,
  SourceSnapshot,
  StateSource,
  TraceRef,
} from './protocol.js'
export type * from './protocol.js'

export interface ClientOptions {
  loggerProvider: LoggerProvider
  captureUnhandled?: boolean
  sanitize?: Sanitizer
  filter?: CaptureFilter
  rateLimit?: RateLimit
  exceptionLimits?: ExceptionLimits
  history?: { enabled?: boolean; maxEntries?: number; maxAgeMs?: number | null }
  onDiagnostic?: (diagnostic: CaptureDiagnostic) => void
  flush?: () => Promise<void>
}
export interface CaptureOptions {
  state?: JsonValue
  serializeState?: (value: unknown) => JsonValue
  context?: Context
  includeRegisteredState?: boolean
  includeHistory?: boolean
  handled?: boolean
  groupKey?: string
  extensions?: Readonly<Record<string, JsonValue>>
  attributes?: LogAttributes
  severityNumber?: SeverityNumber
  severityText?: string
}
export interface SnapshotOptions {
  includeRegisteredState?: boolean
  includeHistory?: boolean
  /** Keep the newest N items after normal age/capacity retention. */
  maxHistoryEntries?: number
  /** UTF-8 JSON budget, default 64 KiB. Required metadata may exceed a tiny budget. */
  maxBytes?: number
  context?: Context
}
export type SnapshotResult =
  | { status: 'ok'; value: DebugSnapshotV1 }
  | { status: 'unavailable'; reason: 'closed' | 'reentrant' }
export type HistoryResult =
  | { status: 'recorded'; id: string }
  | { status: 'not_recorded'; reason: string }
const ownerKey = Symbol.for('backplane.errors.auto-handlers.v1')
const nano = () => (BigInt(Date.now()) * 1_000_000n).toString()
const mono = () => performance.now()
const id = () => crypto.randomUUID()

function correlation(explicit?: Context): { context: Context; trace?: TraceRef } {
  for (const [ctx, origin] of [
    [explicit, 'explicit'],
    [context.active(), 'active'],
  ] as const) {
    if (!ctx) continue
    const sc = trace.getSpanContext(ctx)
    if (
      sc &&
      isSpanContextValid(sc) &&
      /^[0-9a-f]{32}$/.test(sc.traceId) &&
      /^[0-9a-f]{16}$/.test(sc.spanId)
    ) {
      // Snapshot the span context before calling application readers.
      return {
        context: trace.setSpanContext(ROOT_CONTEXT, { ...sc }),
        trace: { traceId: sc.traceId, spanId: sc.spanId, traceFlags: sc.traceFlags, origin },
      }
    }
  }
  return { context: ROOT_CONTEXT }
}
export function createClient(options: ClientOptions) {
  const logger = options.loggerProvider.getLogger('app-debug.browser', SDK_VERSION)
  const exceptionData = exceptionReader(options.exceptionLimits)
  const allowCapture = tokenBucket(options.rateLimit)
  const counts = {
    snapshots: 0,
    attempted: 0,
    emitted: 0,
    filtered: 0,
    rateLimited: 0,
    failed: 0,
    reentrant: 0,
    lastCaptureMs: 0,
    maxCaptureMs: 0,
  }
  const diagnosticCounts: Record<string, number> = Object.create(null)
  let captureDiagnostics: CaptureDiagnostic[] | undefined
  let snapshotDiagnostics: CaptureDiagnostic[] | undefined
  let policyRunning = false
  const enabled = options.history?.enabled ?? true
  const maxEntries = options.history?.maxEntries ?? 100
  const maxAgeMs = options.history?.maxAgeMs === undefined ? 30_000 : options.history.maxAgeMs
  if (
    !Number.isSafeInteger(maxEntries) ||
    maxEntries < 0 ||
    (maxAgeMs !== null && (!Number.isFinite(maxAgeMs) || maxAgeMs < 0))
  )
    throw new TypeError('invalid_history_config')
  let runtimeId = id(),
    sequence = 0,
    since = nano(),
    evicted = 0
  let history: HistoryEntry[] = []
  let closed = false,
    capturing = false,
    diagnosing = false
  const cleanups = new Set<() => void>()
  const reading = new Set<string>()
  const sources = new Map<
    string,
    { registrationId: string; source: { read(): unknown; serialize?(value: unknown): JsonValue } }
  >()
  function diagnostic(
    code: string,
    stage: CaptureDiagnostic['stage'] = 'capture'
  ): CaptureDiagnostic {
    const d = { code, stage }
    // Local reads report their errors in the result, without capture counters or callbacks.
    if (snapshotDiagnostics) {
      if (
        !snapshotDiagnostics.some((item) => item.code === code) &&
        snapshotDiagnostics.length < 64
      )
        snapshotDiagnostics.push(d)
      return d
    }
    diagnosticCounts[code] = (diagnosticCounts[code] ?? 0) + 1
    if (
      captureDiagnostics &&
      !captureDiagnostics.some((item) => item.code === code) &&
      captureDiagnostics.length < 64
    )
      captureDiagnostics.push(d)
    if (!diagnosing) {
      diagnosing = true
      try {
        options.onDiagnostic?.(d)
      } catch {
        /* Never capture diagnostic failures. */
      } finally {
        diagnosing = false
      }
    }
    return d
  }
  function clean(value: unknown, area: DataArea, name?: string): JsonValue {
    const copy = materialize(value)
    if (!options.sanitize) return copy
    policyRunning = true
    try {
      return materialize(options.sanitize(copy, { area, name }))
    } finally {
      policyRunning = false
    }
  }
  function label(value: string): string {
    try {
      const output = clean(value, 'label')
      if (typeof output !== 'string' || !output) throw new TypeError('invalid_label')
      return output
    } catch {
      diagnostic('sanitize_failed')
      return '[REDACTED]'
    }
  }
  function cleanException(input: ExceptionData): ExceptionData {
    if (!options.sanitize) return input
    try {
      const visit = (
        node: import('./protocol.js').ExceptionInfo
      ): import('./protocol.js').ExceptionInfo => {
        const result = { ...node }
        for (const key of ['type', 'message', 'stacktrace'] as const) {
          if (node[key] === undefined) continue
          const text = clean(node[key], 'exception', key)
          if (typeof text !== 'string') throw new TypeError('invalid_exception_text')
          result[key] = text
        }
        if (node.cause) result.cause = visit(node.cause)
        if (node.errors) result.errors = node.errors.map(visit)
        return result
      }
      const result = { ...input, ...visit(input) }
      if (input.location?.url !== undefined) {
        const url = clean(input.location.url, 'exception', 'url')
        if (typeof url !== 'string') throw new TypeError('invalid_location')
        result.location = { ...input.location, url }
      }
      return result
    } catch {
      diagnostic('sanitize_failed')
      return {
        mechanism: input.mechanism,
        ...(input.handled === undefined ? {} : { handled: input.handled }),
        incomplete: 'unreadable',
      }
    }
  }
  function clearHistory() {
    history = []
    evicted = 0
    since = nano()
  }
  function nextSequence() {
    if (sequence >= Number.MAX_SAFE_INTEGER) {
      runtimeId = id()
      sequence = 0
      clearHistory()
    }
    return ++sequence
  }
  function prune(now: number) {
    if (maxAgeMs !== null) {
      const kept = history.filter((item) => now - item.monotonicMs <= maxAgeMs)
      evicted += history.length - kept.length
      history = kept
    }
    if (history.length > maxEntries) {
      evicted += history.length - maxEntries
      history = history.slice(history.length - maxEntries)
    }
  }
  function captured(
    value: unknown,
    serializer?: (value: unknown) => JsonValue,
    area: DataArea = 'inline',
    name?: string
  ): CapturedValue {
    if (serializer) {
      try {
        value = serializer(value)
      } catch {
        return { status: 'error', error: diagnostic('serializer_failed', 'serialize') }
      }
    }
    try {
      return { status: 'ok', value: clean(value, area, name) }
    } catch {
      return {
        status: 'error',
        error: diagnostic(
          options.sanitize ? 'sanitize_or_json_failed' : 'invalid_json',
          'validate'
        ),
      }
    }
  }
  function snapshot(
    name: string,
    registration: NonNullable<ReturnType<typeof sources.get>>
  ): SourceSnapshot {
    const base = {
      name: label(name),
      registrationId: registration.registrationId,
      capturedAtUnixNano: nano(),
      monotonicMs: mono(),
    }
    if (reading.has(registration.registrationId))
      return { ...base, status: 'error', error: diagnostic('reentrant_read', 'read') }
    reading.add(registration.registrationId)
    try {
      let value: unknown
      try {
        value = registration.source.read()
      } catch {
        return { ...base, status: 'error', error: diagnostic('reader_failed', 'read') }
      }
      return { ...base, ...captured(value, registration.source.serialize, 'source', name) }
    } finally {
      reading.delete(registration.registrationId)
    }
  }
  function registerState(name: string, source: JsonStateSource): () => void
  function registerState<T>(name: string, source: StateSource<T>): () => void
  function registerState(
    name: string,
    source: { read(): unknown; serialize?(value: unknown): JsonValue }
  ) {
    if (
      closed ||
      !name ||
      sources.has(name) ||
      typeof source.read !== 'function' ||
      (source.serialize !== undefined && typeof source.serialize !== 'function')
    )
      throw new TypeError('invalid_or_duplicate_registration')
    const registration = { registrationId: id(), source }
    sources.set(name, registration)
    return () => {
      if (sources.get(name) === registration) sources.delete(name)
    }
  }
  function add(
    kind: 'breadcrumb' | 'state',
    name: string,
    data?: JsonValue,
    ctx?: Context
  ): HistoryResult {
    if (closed || diagnosing || policyRunning || !enabled || !name) {
      diagnostic('history_unavailable')
      return { status: 'not_recorded', reason: 'closed_disabled_or_invalid' }
    }
    const registration = sources.get(name)
    if (kind === 'state' && (!registration || reading.has(registration.registrationId))) {
      diagnostic('source_unavailable')
      return { status: 'not_recorded', reason: 'source_unavailable' }
    }
    const base = {
      id: id(),
      sequence: nextSequence(),
      timestampUnixNano: nano(),
      monotonicMs: mono(),
      trace: correlation(ctx).trace,
    }
    const item: HistoryEntry =
      kind === 'state'
        ? {
            ...base,
            kind,
            snapshot: snapshot(name, registration as NonNullable<typeof registration>),
          }
        : {
            ...base,
            kind,
            name: label(name),
            ...(data === undefined ? {} : { data: captured(data, undefined, 'breadcrumb', name) }),
          }
    history.push(item)
    prune(mono())
    return { status: 'recorded', id: item.id }
  }
  function localSnapshot(opts: SnapshotOptions = {}): SnapshotResult {
    if (closed) return { status: 'unavailable', reason: 'closed' }
    if (capturing || diagnosing || policyRunning || reading.size > 0)
      return { status: 'unavailable', reason: 'reentrant' }
    capturing = true
    snapshotDiagnostics = []
    try {
      const timestampUnixNano = nano(),
        monotonicMs = mono(),
        runtime = { id: runtimeId, sequence }
      const link = correlation(opts.context)
      prune(monotonicMs)
      const retained = {
        enabled,
        sinceUnixNano: since,
        evictedCount: evicted,
        truncatedCount: 0,
        items: opts.includeHistory === false ? [] : [...history],
      }
      const registrations = [...sources]
      const value = budgetSnapshot(
        {
          schema: 'app-debug',
          schemaVersion: 1,
          kind: 'snapshot',
          snapshotId: id(),
          timestampUnixNano,
          monotonicMs,
          runtime,
          ...(link.trace ? { trace: link.trace } : {}),
          state: {
            sources:
              opts.includeRegisteredState === false
                ? []
                : registrations.map(([name, registration]) => snapshot(name, registration)),
          },
          history: retained,
          diagnostics: snapshotDiagnostics,
        },
        opts
      )
      counts.snapshots++
      return { status: 'ok', value }
    } finally {
      snapshotDiagnostics = undefined
      capturing = false
    }
  }
  function capture(
    value: unknown,
    opts: CaptureOptions = {},
    mechanism: ExceptionData['mechanism'] = 'manual',
    location?: ExceptionData['location']
  ): CaptureResult {
    counts.attempted++
    if (closed) {
      counts.failed++
      return { status: 'not_emitted', reason: 'closed' }
    }
    if (capturing || diagnosing || policyRunning) {
      counts.reentrant++
      return { status: 'not_emitted', reason: 'reentrant' }
    }
    capturing = true
    const started = mono()
    captureDiagnostics = []
    try {
      if (!allowCapture()) {
        counts.filtered++
        counts.rateLimited++
        diagnostic('rate_limited')
        return { status: 'not_emitted', reason: 'filtered' }
      }
      const timestamp = nano(),
        monotonicMs = mono(),
        seq = nextSequence()
      const link = correlation(opts.context)
      prune(monotonicMs)
      const retained = {
        enabled,
        sinceUnixNano: since,
        evictedCount: evicted,
        items: opts.includeHistory === false ? [] : [...history],
      }
      const registrations = [...sources]
      const exception = cleanException({
        ...exceptionData(value, mechanism, mechanism === 'manual' ? opts.handled : false),
        ...(location ? { location } : {}),
      })
      if (options.filter) {
        let accepted = false
        policyRunning = true
        try {
          accepted = options.filter(materialize(exception) as unknown as ExceptionData) === true
        } catch {
          diagnostic('filter_failed')
        } finally {
          policyRunning = false
        }
        if (!accepted) {
          counts.filtered++
          return { status: 'not_emitted', reason: 'filtered' }
        }
      }
      let extensions: Record<string, JsonValue> | undefined
      if (opts.extensions !== undefined) {
        try {
          const data = clean(opts.extensions, 'extensions')
          if (!data || typeof data !== 'object' || Array.isArray(data))
            throw new TypeError('invalid_extensions')
          extensions = data as Record<string, JsonValue>
        } catch {
          diagnostic('extensions_omitted')
        }
      }
      const envelope: DebugEnvelopeV1 = {
        schema: 'app-debug',
        schemaVersion: 1,
        kind: 'exception',
        eventId: id(),
        timestampUnixNano: timestamp,
        monotonicMs,
        runtime: { id: runtimeId, sequence: seq },
        exception,
        ...(link.trace ? { trace: link.trace } : {}),
        state: {
          sources:
            opts.includeRegisteredState === false
              ? []
              : registrations.map(([name, source]) => snapshot(name, source)),
          ...('state' in opts ? { inline: captured(opts.state, opts.serializeState) } : {}),
        },
        history: retained,
        ...(opts.groupKey === undefined ? {} : { groupKey: label(opts.groupKey) }),
        ...(extensions === undefined ? {} : { extensions }),
        diagnostics: captureDiagnostics,
      }
      if (opts.groupKey !== undefined && !opts.groupKey) throw new TypeError('empty_group_key')
      if (
        opts.severityNumber !== undefined &&
        (!Number.isInteger(opts.severityNumber) ||
          opts.severityNumber < 0 ||
          opts.severityNumber > 24)
      )
        throw new TypeError('invalid_severity')
      const attrs: LogAttributes = Object.create(null)
      if (opts.attributes) {
        let user: Record<string, JsonValue> = Object.create(null)
        try {
          const data = clean(opts.attributes, 'attributes')
          if (!data || typeof data !== 'object' || Array.isArray(data))
            throw new TypeError('invalid_attributes')
          user = data as Record<string, JsonValue>
        } catch {
          diagnostic('attributes_omitted')
        }
        for (const [key, val] of Object.entries(user)) {
          if (
            /^(app\.debug\.|exception\.|_|trace_id$|span_id$|severity_|event_name$|scope\.|service\.|deployment\.)/.test(
              key
            )
          ) {
            diagnostic('reserved_attribute')
            continue
          }
          attrs[key] = val as LogAttributes[string]
        }
      }
      Object.assign(attrs, {
        'app.debug.schema.version': 1,
        'app.debug.kind': 'exception',
        'app.debug.event.id': envelope.eventId,
        'app.debug.runtime.id': envelope.runtime.id,
        'app.debug.event.sequence': seq,
        'app.debug.exception.mechanism': mechanism,
      })
      for (const key of ['type', 'message', 'stacktrace'] as const)
        if (exception[key] !== undefined) attrs[`exception.${key}`] = exception[key]
      if (exception.handled !== undefined) attrs['app.debug.exception.handled'] = exception.handled
      if (envelope.groupKey !== undefined) attrs['app.debug.group.key'] = envelope.groupKey
      const body = JSON.stringify(envelope)
      const n = BigInt(timestamp),
        time: [number, number] = [Number(n / 1_000_000_000n), Number(n % 1_000_000_000n)]
      try {
        logger.emit({
          eventName: 'exception',
          timestamp: time,
          observedTimestamp: time,
          context: link.context,
          severityNumber: opts.severityNumber ?? SeverityNumber.ERROR,
          severityText:
            opts.severityText ?? (opts.severityNumber === undefined ? 'ERROR' : undefined),
          body,
          attributes: attrs,
        })
      } catch {
        counts.failed++
        diagnostic('emit_failed')
        return { status: 'not_emitted', reason: 'emit_failed' }
      }
      counts.emitted++
      return { status: 'emitted', eventId: envelope.eventId }
    } catch {
      counts.failed++
      diagnostic('encode_failed')
      return { status: 'not_emitted', reason: 'encode_failed' }
    } finally {
      counts.lastCaptureMs = mono() - started
      counts.maxCaptureMs = Math.max(counts.maxCaptureMs, counts.lastCaptureMs)
      captureDiagnostics = undefined
      capturing = false
    }
  }
  let removeHandlers = () => {}
  // A page's window or a worker's self: both report uncaught errors and unhandled rejections.
  const scope = globalScope()
  if (options.captureUnhandled && scope) {
    const host = scope as unknown as Record<symbol, unknown>
    if (host[ownerKey]) diagnostic('auto_handlers_already_owned')
    else {
      const owner = {}
      host[ownerKey] = owner
      const onError = (event: ErrorEvent) => {
        if (!(event instanceof ErrorEvent) || event.target !== scope) return
        capture(event.error ?? event.message, {}, 'window.error', {
          ...(event.filename ? { url: event.filename } : {}),
          line: event.lineno,
          column: event.colno,
        })
      }
      const onRejection = (event: PromiseRejectionEvent) => {
        capture(event.reason, {}, 'unhandledrejection')
      }
      scope.addEventListener('error', onError)
      scope.addEventListener('unhandledrejection', onRejection)
      removeHandlers = () => {
        scope.removeEventListener('error', onError)
        scope.removeEventListener('unhandledrejection', onRejection)
        if (host[ownerKey] === owner) delete host[ownerKey]
      }
    }
  }
  return {
    onDispose: (cleanup: () => void) => {
      if (closed) cleanup()
      else cleanups.add(cleanup)
      return () => {
        cleanups.delete(cleanup)
      }
    },
    registerState,
    snapshot: localSnapshot,
    captureException: (value: unknown, opts?: CaptureOptions) => capture(value, opts),
    addBreadcrumb: (name: string, data?: JsonValue, opts?: { context?: Context }) =>
      add('breadcrumb', name, data, opts?.context),
    recordState: (name: string, opts?: { context?: Context }) =>
      add('state', name, undefined, opts?.context),
    clearHistory,
    stats: (): ClientStats => ({
      ...counts,
      historyEntries: history.length,
      historyEvicted: evicted,
      diagnostics: { ...diagnosticCounts },
    }),
    flush: async () => {
      await options.flush?.()
    },
    dispose: () => {
      if (!closed) {
        closed = true
        removeHandlers()
        for (const cleanup of cleanups) {
          try {
            cleanup()
          } catch {
            diagnostic('cleanup_failed')
          }
        }
        cleanups.clear()
        sources.clear()
        clearHistory()
      }
    },
  }
}
