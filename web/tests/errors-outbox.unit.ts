import { createTraceState } from '@opentelemetry/api'
import { resourceFromAttributes } from '@opentelemetry/resources'
import type { ReadableLogRecord } from '@opentelemetry/sdk-logs'
import { expect, it, vi } from 'vitest'
import { PersistentLogProcessor } from '../packages/errors/src/outbox'
import { decodeRecord, encodeRecord } from '../packages/errors/src/outbox-record'
import { outboxLimits, type OutboxStorage, type StoredExport } from '../packages/errors/src/ports'

it('durable DTO preserves exact Body, native times, attributes, scope, Resource and unsampled context', () => {
  const attributes = JSON.parse(
    '{"__proto__":{"polluted":true},"dots.key":[null,false,0,"",[],{},"世界"]}'
  )
  const original: ReadableLogRecord = {
    hrTime: [1000, 123456789],
    hrTimeObserved: [1001, 987654321],
    body: '{ "exact whitespace": "<script>untrusted</script>" }',
    eventName: 'exception',
    severityNumber: 17,
    severityText: 'ERROR',
    attributes,
    droppedAttributesCount: 0,
    resource: resourceFromAttributes(
      { 'service.name': 'original-service', zero: 0, empty: '' },
      { schemaUrl: 'https://schema.example/resource' }
    ),
    instrumentationScope: {
      name: 'app-debug.browser',
      version: '0.1.0',
      schemaUrl: 'https://schema.example/scope',
      attributes: { instrumented: false },
      droppedAttributesCount: 0,
    },
    spanContext: {
      traceId: 'a'.repeat(32),
      spanId: 'b'.repeat(16),
      traceFlags: 0,
      isRemote: true,
      traceState: createTraceState('vendor=value'),
    },
  }
  const payload = encodeRecord(original)
  attributes['dots.key'][0] = 'changed after emit'
  const restored = decodeRecord(payload)
  expect(restored.body).toBe(original.body)
  expect(restored.hrTime).toEqual(original.hrTime)
  expect(restored.hrTimeObserved).toEqual(original.hrTimeObserved)
  expect(restored.attributes['dots.key']).toEqual([null, false, 0, '', [], {}, '世界'])
  expect(Object.prototype).not.toHaveProperty('polluted')
  expect(restored.resource.attributes).toEqual(original.resource.attributes)
  expect(restored.resource.schemaUrl).toBe(original.resource.schemaUrl)
  expect(restored.instrumentationScope).toEqual(original.instrumentationScope)
  expect(restored.spanContext).toMatchObject({ traceFlags: 0, isRemote: true })
  expect(restored.spanContext?.traceState?.serialize()).toBe('vendor=value')
  for (const change of [
    { severityNumber: Infinity },
    { scopeVersion: {} },
    { hrTime: [-1, 0] },
    { body: {} },
    { spanContext: { traceId: 'bad' } },
  ]) {
    expect(() => decodeRecord(JSON.stringify({ ...JSON.parse(payload), ...change }))).toThrow()
  }
  expect(() => encodeRecord({ ...original, body: { structured: true } })).toThrow()
})

const sample: ReadableLogRecord = {
  hrTime: [1, 0],
  hrTimeObserved: [1, 0],
  body: '{"kind":"exception"}',
  attributes: {},
  droppedAttributesCount: 0,
  resource: resourceFromAttributes({ 'service.name': 'ported' }),
  instrumentationScope: { name: 'app-debug.browser' },
}

// A storage held in memory: the contract of OutboxStorage, as a host without IndexedDB implements it.
function memoryStorage(now: () => number): OutboxStorage {
  const rows = new Map<string, StoredExport>()
  return {
    limits: { maxBytes: 1 << 20, maxEntries: 100, maxAgeMs: 60_000 },
    put: async (entry) => {
      rows.set(entry.id, { ...entry })
      return { stored: true, evicted: 0 }
    },
    claim: async (owner, force) => {
      const entries = [...rows.values()].filter(
        (row) => (!row.lease || row.lease <= now()) && (force || row.next <= now())
      )
      for (const row of entries) Object.assign(row, { owner, lease: now() + 30_000 })
      return { entries: entries.map((row) => ({ ...row })), expired: 0 }
    },
    finish: async (entries, owner, success) => {
      for (const entry of entries) {
        const row = rows.get(entry.id)
        if (row?.owner !== owner) continue
        if (success) rows.delete(entry.id)
        else Object.assign(row, { attempts: row.attempts + 1, owner: undefined, lease: undefined })
      }
    },
    stats: async () => ({
      entries: rows.size,
      bytes: [...rows.values()].reduce((sum, row) => sum + row.bytes, 0),
      oldestAgeMs: 0,
    }),
    clear: async () => rows.clear(),
    close: () => {},
  }
}

it('outbox runs on ports alone: no window, document, location or crypto', async () => {
  expect(typeof window).toBe('undefined')
  expect(typeof document).toBe('undefined')
  vi.stubGlobal('crypto', undefined)
  vi.stubGlobal('location', undefined)
  const sent: string[] = []
  let kick: ((urgent: boolean) => void) | undefined
  const untrigger = vi.fn()
  let ids = 0
  try {
    const outbox = new PersistentLogProcessor(
      {
        export: (records, done) => {
          sent.push(...records.map((record) => String(record.body)))
          done({ code: 0 })
        },
        shutdown: () => Promise.resolve(),
        forceFlush: () => Promise.resolve(),
      },
      {
        storage: memoryStorage(() => 1000),
        triggers: {
          subscribe: (fn) => {
            kick = fn
            return untrigger
          },
        },
      },
      { now: () => 1000, monotonic: () => 0, randomId: () => `id-${++ids}` },
      () => {}
    )
    const record = decodeRecord(encodeRecord(sample))
    outbox.onEmit(record)
    await outbox.flushStorage()
    kick?.(true)
    await outbox.forceFlush()
    expect(sent).toEqual([sample.body])
    expect(await outbox.stats()).toMatchObject({ entries: 0, persisted: 1, accepted: 1 })
    await outbox.shutdown()
    expect(untrigger).toHaveBeenCalledOnce()
  } finally {
    vi.unstubAllGlobals()
  }
})

it('outbox limits require an explicit namespace and finite positive budgets', () => {
  expect(outboxLimits({ name: 'app-account-42' })).toEqual({
    name: 'app-account-42',
    maxBytes: 10 * 1024 * 1024,
    maxEntries: 1000,
    maxAgeMs: 86400000,
  })
  for (const options of [
    { name: '' },
    { name: 'account/a' },
    { name: 'a', maxEntries: 0 },
    { name: 'a', maxBytes: Infinity },
    { name: 'a', maxAgeMs: -1 },
  ]) {
    expect(() => outboxLimits(options)).toThrow()
  }
})
