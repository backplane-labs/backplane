import { readFileSync } from 'node:fs'
import { context, ROOT_CONTEXT, trace } from '@opentelemetry/api'
import type { LogRecord } from '@opentelemetry/api-logs'
import Ajv from 'ajv/dist/2020'
import { afterEach, expect, it, vi } from 'vitest'
import {
  createClient,
  redactKeys,
  type ClientOptions,
  type SnapshotOptions,
  type SnapshotResult,
} from '../packages/errors/src/index'
import { nodeRuntime } from './errors-runtime'
import type { DebugEnvelopeV1, DebugSnapshotV1 } from '../packages/errors/src/protocol'

function setup(options: Partial<ClientOptions> = {}) {
  const records: LogRecord[] = []
  const emit = vi.fn((record: LogRecord) => records.push(record))
  const client = createClient({
    ...options,
    loggerProvider: options.loggerProvider ?? { getLogger: () => ({ emit, enabled: () => true }) },
    runtime: options.runtime ?? nodeRuntime,
  })
  return {
    client,
    emit,
    records,
    capture: () => {
      expect(client.captureException('original')).toMatchObject({ status: 'emitted' })
      return JSON.parse(String(records.at(-1)?.body)) as DebugEnvelopeV1
    },
    snapshot: (opts?: SnapshotOptions): DebugSnapshotV1 => {
      const result = client.snapshot(opts)
      if (result.status !== 'ok') throw Error(result.reason)
      return result.value
    },
  }
}
const bytes = (value: unknown) => new TextEncoder().encode(JSON.stringify(value)).byteLength
function freezeTime() {
  vi.spyOn(Date, 'now').mockReturnValue(1_750_000_000_000)
  return vi.spyOn(performance, 'now').mockReturnValue(100)
}
afterEach(() => vi.restoreAllMocks())

it('uses capture source materialization and history exactly, including failures and app logs', () => {
  freezeTime()
  const sanitize = redactKeys(['password'])
  const { client, snapshot, capture } = setup({ sanitize })
  client.registerState('editor', {
    read: () => new Map([['password', 'SECRET']]),
    serialize: (value) => Object.fromEntries(value),
  })
  client.registerState('failed', {
    read: () => {
      throw Error('private reader error')
    },
  })
  client.recordState('editor')
  client.addBreadcrumb('log.editor', { password: 'SECRET', message: 'saved' })
  const local = snapshot()
  const envelope = capture()
  expect(local.state.sources).toEqual(envelope.state.sources)
  expect(local.history.items).toEqual(envelope.history.items)
  expect(local.state.sources[0]).toMatchObject({ value: { password: '[REDACTED]' } })
  expect(JSON.stringify(local)).not.toContain('SECRET')
  expect(local.history).toMatchObject({ enabled: true, truncatedCount: 0, evictedCount: 0 })
  expect(local.diagnostics).toContainEqual({ code: 'reader_failed', stage: 'read' })
})

it('does not emit, invoke filter/diagnostic hooks, consume budget or alter capture counters', () => {
  freezeTime()
  const filter = vi.fn(() => true),
    onDiagnostic = vi.fn()
  const { client, snapshot, emit } = setup({
    filter,
    onDiagnostic,
    rateLimit: { burst: 1, perSecond: 0.001 },
  })
  client.registerState('broken', {
    read: () => 1,
    serialize: () => {
      throw Error('serializer')
    },
  })
  const before = client.stats()
  snapshot()
  snapshot({ maxBytes: 0 })
  expect(client.stats()).toEqual({ ...before, snapshots: 2 })
  expect(emit).not.toHaveBeenCalled()
  expect(filter).not.toHaveBeenCalled()
  expect(onDiagnostic).not.toHaveBeenCalled()
  expect(client.captureException('budget still available').status).toBe('emitted')
  expect(client.captureException('now exhausted').status).toBe('not_emitted')
})

it('leaves no snapshot in history and does not advance the runtime event sequence', () => {
  const { client, snapshot, capture } = setup()
  client.addBreadcrumb('before')
  const first = snapshot(),
    second = snapshot(),
    envelope = capture()
  expect(first.snapshotId).not.toBe(second.snapshotId)
  expect(first.runtime).toEqual(second.runtime)
  expect(first.runtime.sequence).toBe(1)
  expect(envelope.runtime.sequence).toBe(2)
  expect(envelope.history.items).toEqual(first.history.items)
})

it('blocks snapshot recursion from state readers, serializers, sanitize, filter and diagnostic hooks', () => {
  const nested: SnapshotResult[] = []
  const { client, snapshot, capture } = setup({
    sanitize: (value) => {
      nested.push(client.snapshot())
      return value
    },
    filter: () => {
      nested.push(client.snapshot())
      return true
    },
    onDiagnostic: () => nested.push(client.snapshot()),
  })
  client.registerState('reader', {
    read: () => {
      nested.push(client.snapshot())
      return 0
    },
    serialize: (value: number) => {
      nested.push(client.snapshot())
      return value
    },
  })
  client.recordState('reader') // per-source reading guard, outside capture
  client.addBreadcrumb('sanitizer') // policy guard, outside capture
  snapshot()
  capture()
  client.recordState('missing') // diagnostic guard, outside capture
  expect(nested.length).toBeGreaterThan(10)
  expect(nested.every((r) => r.status === 'unavailable' && r.reason === 'reentrant')).toBe(true)
  expect(client.stats().snapshots).toBe(1)
})

it('returns unavailable after disposal, and disabled/omitted history remains an ok empty result', () => {
  const { client, snapshot } = setup({ history: { enabled: false } })
  const read = vi.fn(() => false)
  client.registerState('state', { read })
  expect(snapshot().history).toMatchObject({ enabled: false, items: [], truncatedCount: 0 })
  expect(snapshot({ includeRegisteredState: false }).state.sources).toEqual([])
  expect(read).toHaveBeenCalledOnce()
  client.dispose()
  const before = client.stats()
  expect(client.snapshot()).toEqual({ status: 'unavailable', reason: 'closed' })
  expect(client.stats()).toEqual(before)
  const other = setup()
  other.client.addBreadcrumb('retained')
  expect(other.snapshot({ includeHistory: false }).history).toMatchObject({
    enabled: true,
    items: [],
    truncatedCount: 0,
  })
  expect(other.snapshot().history.items).toHaveLength(1)
})

it('cuts before callbacks; age/capacity eviction and snapshot truncation are separate', () => {
  const now = freezeTime()
  const { client, snapshot } = setup({ history: { maxEntries: 3, maxAgeMs: 20 } })
  client.addBreadcrumb('capacity-evicted')
  client.addBreadcrumb('expired')
  now.mockReturnValue(110)
  client.addBreadcrumb('old')
  client.addBreadcrumb('new')
  now.mockReturnValue(125)
  client.registerState('adds-history', {
    read: () => {
      client.addBreadcrumb('after-cut')
      return null
    },
  })
  const value = snapshot({ maxHistoryEntries: 1 })
  expect(value.history).toMatchObject({ evictedCount: 2, truncatedCount: 1 })
  expect(value.history.items).toMatchObject([{ name: 'new' }])
  expect(snapshot({ includeRegisteredState: false }).history.items).toMatchObject([
    { name: 'old' },
    { name: 'new' },
    { name: 'after-cut' },
  ])
  client.clearHistory()
  expect(snapshot({ includeRegisteredState: false }).history).toMatchObject({
    items: [],
    evictedCount: 0,
    truncatedCount: 0,
  })
})

it('returns detached JSON and structuredClone-safe values including prototype-like keys', () => {
  const { client, snapshot, capture } = setup()
  const state = JSON.parse(
    '{"__proto__":{"polluted":true},"key.with.dots":[null,false,0,"",[],{},"世界"]}'
  )
  client.registerState('state', { read: () => state })
  client.recordState('state')
  client.addBreadcrumb('log.app', state)
  const value = snapshot()
  const expected = JSON.stringify(value)
  expect(JSON.stringify(structuredClone(value))).toBe(expected)
  expect(JSON.stringify(JSON.parse(expected))).toBe(expected)
  const history = capture().history.items
  // Simulate an untyped consumer freely modifying the returned DTO.
  Object.assign(value.state.sources[0], { value: 'changed' })
  Object.assign(value.history.items[0], { snapshot: {} })
  Object.assign(value.history.items[1], { data: {} })
  expect(capture().history.items).toEqual(history)
  expect(snapshot().state.sources[0]).toMatchObject({ value: state })
  expect(Object.prototype).not.toHaveProperty('polluted')
})

it('rejects unsafe source values without calling getters or toJSON and recovers on the next call', () => {
  const { client, snapshot } = setup()
  const getter = vi.fn(),
    toJSON = vi.fn()
  const unregister = client.registerState('getters', {
    read: () => Object.defineProperty({}, 'secret', { get: getter, enumerable: true }),
  })
  client.registerState('toJSON', { read: () => ({ toJSON }) as never })
  expect(snapshot().state.sources.map((s) => s.status)).toEqual(['error', 'error'])
  expect(getter).not.toHaveBeenCalled()
  expect(toJSON).not.toHaveBeenCalled()
  unregister()
  client.registerState('getters', { read: () => null })
  expect(snapshot().state.sources[1]).toMatchObject({ status: 'ok', value: null })
})

it('drops the oldest history first, fits UTF-8 bytes and never trims retained capture data', () => {
  freezeTime()
  const { client, snapshot, capture } = setup()
  client.registerState('small', { read: () => '世界' })
  client.addBreadcrumb('old', '🙂'.repeat(500))
  client.addBreadcrumb('middle', '🙂'.repeat(500))
  client.addBreadcrumb('new', '🙂'.repeat(500))
  const all = snapshot()
  const desired = {
    ...all,
    history: { ...all.history, items: all.history.items.slice(2), truncatedCount: 2 },
    diagnostics: [{ code: 'snapshot_budget', stage: 'serialize' }],
  }
  const budget = bytes(desired)
  const value = snapshot({ maxBytes: budget })
  expect(bytes(value)).toBe(budget)
  expect(value.history.items).toMatchObject([{ name: 'new' }])
  expect(value.history.truncatedCount).toBe(2)
  expect(value.state.sources).toEqual(all.state.sources)
  expect(value.diagnostics).toContainEqual({ code: 'snapshot_budget', stage: 'serialize' })
  expect(capture().history.items).toEqual(all.history.items)
  expect(snapshot({ maxHistoryEntries: 0 }).history.truncatedCount).toBe(3)
  const combined = snapshot({ maxHistoryEntries: 2, maxBytes: budget })
  expect(combined.history.truncatedCount).toBe(2)
  expect(combined.history.items).toEqual(value.history.items)
})

it('after history, replaces largest source values first while preserving identity and smaller values', () => {
  freezeTime()
  const { client, snapshot, capture } = setup()
  client.registerState('medium', { read: () => 'm'.repeat(1000) })
  client.registerState('largest', { read: () => '🙂'.repeat(2000) })
  client.registerState('small', { read: () => false })
  client.addBreadcrumb('tiny', 0)
  const value = snapshot({ maxBytes: 2500 })
  expect(bytes(value)).toBeLessThanOrEqual(2500)
  expect(value.history).toMatchObject({ items: [], truncatedCount: 1 })
  expect(value.state.sources.map((s) => s.status)).toEqual(['ok', 'error', 'ok'])
  expect(value.state.sources[1]).toMatchObject({
    name: 'largest',
    error: { code: 'budget_exceeded', stage: 'serialize' },
  })
  expect(value.state.sources[1]).not.toHaveProperty('value')
  expect(capture().state.sources[1]).toMatchObject({ status: 'ok', value: '🙂'.repeat(2000) })
})

it('uses 64 KiB by default and returns a valid diagnostic when metadata alone exceeds the budget', () => {
  const { client, snapshot } = setup()
  client.registerState('huge', { read: () => 'x'.repeat(70_000) })
  expect(snapshot().state.sources[0]).toMatchObject({ status: 'error' })
  expect(snapshot({ maxBytes: 100_000 }).state.sources[0]).toMatchObject({ status: 'ok' })
  for (const maxBytes of [0, 1]) {
    const value = snapshot({ maxBytes })
    expect(value.kind).toBe('snapshot')
    expect(value.snapshotId).toBeTruthy()
    expect(value.diagnostics).toContainEqual({ code: 'snapshot_budget', stage: 'serialize' })
    expect(value.state.sources[0]).toMatchObject({ error: { code: 'budget_exceeded' } })
  }
})

it.each([
  -1,
  NaN,
  Infinity,
  0.5,
])('reports invalid budget options %s and uses defaults', (limit) => {
  const { client, snapshot } = setup()
  client.addBreadcrumb('keep')
  const value = snapshot({ maxBytes: limit, maxHistoryEntries: limit })
  expect(value.history.items).toHaveLength(1)
  expect(value.diagnostics).toEqual([{ code: 'snapshot_options', stage: 'capture' }])
})

it('uses the same explicit/active/missing correlation, including unsampled context', () => {
  const { snapshot } = setup()
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
  const current = vi.spyOn(context, 'active').mockReturnValue(active)
  expect(snapshot({ context: explicit }).trace).toMatchObject({
    origin: 'explicit',
    traceId: '3'.repeat(32),
    traceFlags: 0,
  })
  expect(snapshot().trace).toMatchObject({ origin: 'active', traceId: '1'.repeat(32) })
  expect(snapshot({ context: ROOT_CONTEXT }).trace?.origin).toBe('active')
  current.mockReturnValue(ROOT_CONTEXT)
  expect(snapshot()).not.toHaveProperty('trace')
})

it('keeps local snapshots outside the unchanged exception wire schema', () => {
  const schema = JSON.parse(readFileSync('../internal/errors/envelope/app-debug-v1.schema.json', 'utf8'))
  const validate = new Ajv({ strict: false }).compile(schema)
  const { snapshot, capture } = setup()
  expect(validate(snapshot())).toBe(false)
  expect(validate(capture())).toBe(true)
})
