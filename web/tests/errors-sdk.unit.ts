import { readFileSync } from 'node:fs'
import { context, ROOT_CONTEXT, trace, type Context, type ContextManager } from '@opentelemetry/api'
import type { LoggerProvider, LogRecord } from '@opentelemetry/api-logs'
import Ajv from 'ajv/dist/2020'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createClient, type ClientOptions, type UncaughtSource } from '../packages/errors/src/index'
import { materialize } from '../packages/errors/src/json'
import type { DebugEnvelopeV1, JsonValue } from '../packages/errors/src/protocol'
import { nodeRuntime } from './errors-runtime'

const schema = JSON.parse(readFileSync('../internal/errors/envelope/app-debug-v1.schema.json', 'utf8'))
const validate = new Ajv({ strict: false, allErrors: true }).compile(schema)
function setup(options: Partial<ClientOptions> = {}) {
  const records: LogRecord[] = []
  const provider: LoggerProvider = {
    getLogger: () => ({
      enabled: () => true,
      emit: (record) => {
        records.push(record)
      },
    }),
  }
  const client = createClient({
    ...options,
    loggerProvider: options.loggerProvider ?? provider,
    runtime: options.runtime ?? nodeRuntime,
  })
  function body(index = 0) {
    const result = JSON.parse(String(records[index].body)) as DebugEnvelopeV1
    expect(validate(result), JSON.stringify(validate.errors)).toBe(true)
    return result
  }
  return { client, records, body }
}
afterEach(() => {
  vi.restoreAllMocks()
  context.disable()
})

describe('strict immutable JSON', () => {
  it('preserves edge values, Unicode and prototype-like keys without invoking code', () => {
    const value = JSON.parse(
      '{"__proto__":{"polluted":true},"constructor":0,"key.with.dots":[null,false,0,"",[],{},"Привет 世界"]}'
    )
    const copied = materialize(value)
    value['key.with.dots'][0] = 7
    expect(copied).toEqual(
      JSON.parse(
        '{"__proto__":{"polluted":true},"constructor":0,"key.with.dots":[null,false,0,"",[],{},"Привет 世界"]}'
      )
    )
    expect(Object.prototype).not.toHaveProperty('polluted')
  })
  it.each([
    undefined,
    NaN,
    Infinity,
    BigInt(7),
    Symbol('x'),
    () => 1,
    new Date(),
    new Map(),
    new Set(),
    Promise.resolve(1),
    // biome-ignore lint/suspicious/noSparseArray: Deliberate invalid JSON input.
    [, 1],
  ])('rejects unsupported data %s', (value) => {
    expect(() => materialize(value)).toThrow()
  })
  it('rejects accessors, toJSON, sparse arrays, extra array keys, symbols and cycles', () => {
    const getter = vi.fn(() => 1),
      toJSON = vi.fn(() => 1)
    const cyclic: Record<string, unknown> = {}
    cyclic.self = cyclic
    for (const value of [
      Object.defineProperty({}, 'x', { get: getter, enumerable: true }),
      { toJSON },
      cyclic,
      Object.assign([], { x: 1 }),
      { [Symbol('x')]: 1 },
    ])
      expect(() => materialize(value)).toThrow()
    expect(getter).not.toHaveBeenCalled()
    expect(toJSON).not.toHaveBeenCalled()
  })
})

describe('capture and history', () => {
  it('registers dynamically, rejects conflicts and retains immutable snapshots after unregister', () => {
    const { client, body, records } = setup()
    const state = { revision: 1 }
    const read = vi.fn(() => state)
    const remove = client.registerState('editor', { read })
    expect(read).not.toHaveBeenCalled()
    expect(() => client.registerState('editor', { read })).toThrow()
    client.recordState('editor')
    state.revision = 2
    client.captureException(new Error('first'), { state: { editor: 'inline' } })
    const first = body()
    expect(first.state.sources[0]).toMatchObject({ status: 'ok', value: { revision: 2 } })
    expect(first.history.items[0]).toMatchObject({
      kind: 'state',
      snapshot: { value: { revision: 1 } },
    })
    expect(first.state.inline).toEqual({ status: 'ok', value: { editor: 'inline' } })
    remove()
    remove()
    client.registerState('editor', { read })
    client.captureException(new Error('second'))
    expect(body(1).state.sources[0].registrationId).not.toBe(first.state.sources[0].registrationId)
    expect(body(1).history.items[0].id).toBe(first.history.items[0].id)
    expect(records).toHaveLength(2)
  })
  it('isolates callback failures and recursive diagnostics', () => {
    const diagnostics: string[] = []
    const { client, body } = setup({
      onDiagnostic: (event) => {
        diagnostics.push(event.code)
        expect(client.captureException('recursive')).toEqual({
          status: 'not_emitted',
          reason: 'reentrant',
        })
        throw new Error('diagnostic callback')
      },
    })
    client.registerState('read-fails', {
      read: () => {
        throw new Error('reader')
      },
    })
    client.registerState('serialize-fails', {
      read: () => 1,
      serialize: () => {
        throw new Error('serializer')
      },
    })
    client.registerState('valid', { read: () => false })
    expect(client.captureException('original', { state: undefined })).toMatchObject({
      status: 'emitted',
    })
    expect(body().exception.message).toBe('original')
    expect(body().state.sources.map((source) => source.status)).toEqual(['error', 'error', 'ok'])
    expect(body().state.inline?.status).toBe('error')
    expect(diagnostics).toEqual(['reader_failed', 'serializer_failed', 'invalid_json'])
  })
  it('cuts history before readers, retains after emit, and clears counters', () => {
    const { client, body } = setup({ history: { maxEntries: 2, maxAgeMs: null } })
    client.addBreadcrumb('a')
    client.addBreadcrumb('b')
    client.addBreadcrumb('c')
    client.registerState('late', {
      read: () => {
        client.addBreadcrumb('after-cut')
        return null
      },
    })
    client.captureException('cut')
    expect(body().history.items.map((item) => item.kind === 'breadcrumb' && item.name)).toEqual([
      'b',
      'c',
    ])
    expect(body().history.evictedCount).toBe(1)
    client.captureException('without', { includeHistory: false, includeRegisteredState: false })
    expect(body(1).history.items).toEqual([])
    client.captureException('retained', { includeRegisteredState: false })
    expect(body(2).history.items).toHaveLength(2)
    client.clearHistory()
    client.captureException('clear', { includeRegisteredState: false })
    expect(body(3).history.items).toEqual([])
    expect(body(3).history.evictedCount).toBe(0)
  })
  it('evicts by monotonic age rather than wall-clock order', () => {
    const now = vi.spyOn(performance, 'now').mockReturnValue(0)
    const { client, body } = setup({ history: { maxAgeMs: 20 } })
    client.addBreadcrumb('expired', 0)
    now.mockReturnValue(21)
    client.captureException('error')
    expect(body().history.items).toHaveLength(0)
    expect(body().history.evictedCount).toBe(1)
  })
  it('does not cap one snapshot or merge occurrences with the same error', () => {
    const { client, body } = setup()
    client.registerState('large', { read: () => 'x'.repeat(2 << 20) })
    const error = new Error('same')
    const first = client.captureException(error)
    const second = client.captureException(error)
    expect(first).not.toEqual(second)
    expect(body().state.sources[0]).toMatchObject({ status: 'ok', value: 'x'.repeat(2 << 20) })
  })
  it('keeps Body and native OTel attributes consistent and reserves index fields', () => {
    const { client, records, body } = setup()
    const error = new Error('original')
    error.stack = 'unchanged raw stack'
    client.captureException(error, {
      handled: false,
      groupKey: 'group',
      attributes: { 'app.debug.event.id': 'override', _msg: 'override', custom: 7 },
    })
    const envelope = body(),
      record = records[0]
    expect(record.attributes).toMatchObject({
      'app.debug.event.id': envelope.eventId,
      'app.debug.runtime.id': envelope.runtime.id,
      'app.debug.event.sequence': envelope.runtime.sequence,
      'exception.message': envelope.exception.message,
      'exception.stacktrace': 'unchanged raw stack',
      'app.debug.exception.handled': false,
      'app.debug.group.key': 'group',
      custom: 7,
    })
    expect(record.attributes).not.toHaveProperty('_msg')
    expect(record.eventName).toBe('exception')
  })
  it('keeps missing stacks absent and does not serialize a rejection object', () => {
    const { client, body } = setup()
    const toJSON = vi.fn(() => 'secret')
    client.captureException({ message: 'just message', extra: { secret: true }, toJSON })
    expect(body().exception).toEqual({ message: 'just message', mechanism: 'manual' })
    expect(toJSON).not.toHaveBeenCalled()
  })
  it('closes only the client and runs only an explicitly supplied flush', async () => {
    const flush = vi.fn(async () => {})
    const { client } = setup({ flush })
    await client.flush()
    expect(flush).toHaveBeenCalledTimes(1)
    client.dispose()
    client.dispose()
    expect(client.captureException('closed')).toEqual({ status: 'not_emitted', reason: 'closed' })
  })
})

it('uses explicit, active, then missing context; unsampled traces still emit', () => {
  const active = trace.setSpanContext(ROOT_CONTEXT, {
    traceId: '1'.repeat(32),
    spanId: '2'.repeat(16),
    traceFlags: 0,
  })
  const explicit = trace.setSpanContext(ROOT_CONTEXT, {
    traceId: '3'.repeat(32),
    spanId: '4'.repeat(16),
    traceFlags: 0,
  })
  const manager: ContextManager = {
    active: () => active,
    with: <A extends unknown[], F extends (...args: A) => ReturnType<F>>(
      _ctx: Context,
      fn: F,
      thisArg?: ThisParameterType<F>,
      ...args: A
    ) => fn.apply(thisArg, args),
    bind: (_ctx, target) => target,
    enable() {
      return this
    },
    disable() {
      return this
    },
  }
  context.setGlobalContextManager(manager)
  const { client, body } = setup()
  client.captureException('explicit', { context: explicit })
  client.captureException('active')
  context.disable()
  client.captureException('missing')
  expect(body().trace).toMatchObject({ traceId: '3'.repeat(32), origin: 'explicit', traceFlags: 0 })
  expect(body(1).trace).toMatchObject({ traceId: '1'.repeat(32), origin: 'active', traceFlags: 0 })
  expect(body(2).trace).toBeUndefined()
})

it('the envelope fixture follows the schema', () => {
  const fixture = JSON.parse(readFileSync('tests/fixtures/errors/exception-envelope.json', 'utf8'))
  expect(validate(fixture), JSON.stringify(validate.errors)).toBe(true)
  const value: JsonValue = fixture.state.sources[0].value
  expect(materialize(value)).toEqual(value)
})

it('readable OTLP fixture contains the exact envelope in one standard LogRecord', () => {
  const fixture = JSON.parse(readFileSync('tests/fixtures/errors/exception-otlp.json', 'utf8'))
  const records = fixture.resourceLogs[0].scopeLogs[0].logRecords
  expect(records).toHaveLength(1)
  const payload = JSON.parse(records[0].body.stringValue)
  expect(validate(payload), JSON.stringify(validate.errors)).toBe(true)
  expect(payload).toEqual(JSON.parse(readFileSync('tests/fixtures/errors/exception-envelope.json', 'utf8')))
  expect(records[0].traceId).toBe(payload.trace.traceId)
  expect(records[0].timeUnixNano).toBe(payload.timestampUnixNano)
})

describe('host ports', () => {
  it('takes time and ids from the runtime only', () => {
    let ids = 0
    const uuid = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`
    const { client, body } = setup({
      runtime: { now: () => 1_700_000_000_123, monotonic: () => 42, randomId: () => uuid(++ids) },
    })
    client.captureException(new Error('ported'))
    const envelope = body()
    expect(envelope.timestampUnixNano).toBe('1700000000123000000')
    expect(envelope.monotonicMs).toBe(42)
    expect(envelope.runtime.id).toBe(uuid(1))
    expect(envelope.eventId).toBe(uuid(ids))
  })
  it('captures what the uncaught source reports until dispose', () => {
    let report: Parameters<UncaughtSource['subscribe']>[0] | undefined
    const unsubscribe = vi.fn()
    const { client, records, body } = setup({
      uncaught: {
        subscribe: (fn) => {
          report = fn
          return unsubscribe
        },
      },
    })
    report?.({ error: new Error('fatal'), mechanism: 'window.error', location: { line: 3, column: 7 } })
    report?.({ error: 'rejected', mechanism: 'unhandledrejection' })
    expect(body(0).exception).toMatchObject({
      mechanism: 'window.error',
      handled: false,
      message: 'fatal',
      location: { line: 3, column: 7 },
    })
    expect(body(1).exception).toMatchObject({ mechanism: 'unhandledrejection', handled: false })
    client.dispose()
    expect(unsubscribe).toHaveBeenCalledOnce()
    expect(records).toHaveLength(2)
  })
  it('a source that cannot subscribe is a diagnostic, not a failure', () => {
    const diagnostics: string[] = []
    setup({
      uncaught: {
        subscribe: () => {
          throw new Error('taken')
        },
      },
      onDiagnostic: (d) => diagnostics.push(d.code),
    })
    expect(diagnostics).toEqual(['uncaught_unavailable'])
  })
})
