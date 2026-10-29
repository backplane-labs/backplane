import { createTraceState } from '@opentelemetry/api'
import { resourceFromAttributes } from '@opentelemetry/resources'
import type { LogRecordExporter, ReadableLogRecord } from '@opentelemetry/sdk-logs'
import { expect, it, vi } from 'vitest'
import { PersistentLogProcessor } from '../packages/errors/src/outbox'
import { decodeRecord, encodeRecord } from '../packages/errors/src/outbox-record'
import { outboxLimits } from '../packages/errors/src/outbox-store'

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

const exporter: LogRecordExporter = {
  export: (_records, done) => done({ code: 0 }),
  shutdown: () => Promise.resolve(),
  forceFlush: () => Promise.resolve(),
}

it.each([
  { scope: 'a worker: online on its global scope', events: true, want: ['online'] },
  { scope: 'no global events at all', events: false, want: [] },
])('outbox starts and stops without window or document: $scope', ({ events, want }) => {
  const added: string[] = [], removed: string[] = []
  vi.stubGlobal('location', { href: 'https://app.example/' })
  if (events) {
    vi.stubGlobal('addEventListener', (type: string) => added.push(type))
    vi.stubGlobal('removeEventListener', (type: string) => removed.push(type))
  }
  try {
    expect(typeof window).toBe('undefined')
    expect(typeof document).toBe('undefined')
    const outbox = new PersistentLogProcessor(exporter, '/v1/logs', { name: 'worker' }, () => {})
    outbox.stop()
    expect(added).toEqual(want)
    expect(removed).toEqual(want)
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
