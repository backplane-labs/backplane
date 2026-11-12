import { readFileSync } from 'node:fs'
import Ajv from 'ajv/dist/2020'
import { afterEach, expect, it, vi } from 'vitest'
import type { LogRecord } from '@opentelemetry/api-logs'
import {
  createClient,
  createReactErrorHandler,
  redactKeys,
  type ClientOptions,
} from '../packages/errors/src/index'
import { nodeRuntime } from './errors-runtime'
import { deliveryOutcome, retryDelay } from '../packages/errors/src/delivery'
import type { DebugEnvelopeV1, JsonValue } from '../packages/errors/src/protocol'
import { SDK_VERSION } from '../packages/errors/src/version'

const validate = new Ajv({ strict: false }).compile(
  JSON.parse(readFileSync('../internal/errors/envelope/app-debug-v1.schema.json', 'utf8'))
)
function setup(options: Partial<ClientOptions> = {}) {
  const records: LogRecord[] = []
  const getLogger = vi.fn(() => ({
    enabled: () => true,
    emit: (record: LogRecord) => {
      records.push(record)
    },
  }))
  const client = createClient({
    ...options,
    loggerProvider: { getLogger },
    runtime: options.runtime ?? nodeRuntime,
  })
  const body = () => {
    const result = JSON.parse(String(records.at(-1)?.body)) as DebugEnvelopeV1
    expect(validate(result), JSON.stringify(validate.errors)).toBe(true)
    return result
  }
  return { client, records, body, getLogger }
}
afterEach(() => vi.restoreAllMocks())

it('sanitizes before retaining history and preserves mirrored exception attributes', () => {
  const redact = redactKeys(['password', 'token'])
  const { client, records, body } = setup({
    sanitize: (value, ctx) =>
      ctx.area === 'exception' && typeof value === 'string'
        ? value.replaceAll('secret', '[REDACTED]')
        : redact(value, ctx),
  })
  const value = { password: 'secret', token: 'secret', safe: [false, 0, null, '世界'] }
  const remove = client.registerState('source', { read: () => value })
  client.recordState('source')
  client.addBreadcrumb('request', value)
  value.password = 'changed'
  remove()
  client.captureException(new Error('secret', { cause: new Error('secret cause') }), {
    state: value,
    attributes: { token: 'secret' },
    extensions: { user: { password: 'secret' } },
  })
  const envelope = body()
  expect(String(records[0].body)).not.toContain('secret')
  expect(String(records[0].body)).not.toContain('changed')
  expect(records[0].attributes?.['exception.message']).toBe(envelope.exception.message)
  expect(records[0].attributes?.['exception.stacktrace']).toBe(envelope.exception.stacktrace)
  expect(records[0].attributes?.token).toBe('[REDACTED]')
  expect(envelope.history.items).toHaveLength(2)
  expect(envelope.state.inline).toMatchObject({ value: { safe: [false, 0, null, '世界'] } })
})

it('fails closed on sanitizer exceptions without leaking strings or running recursively', () => {
  let client: ReturnType<typeof createClient>
  const fixture = setup({
    sanitize: () => {
      client.captureException('nested')
      throw new Error('secret')
    },
  })
  client = fixture.client
  client.registerState('secret', { read: () => ({ password: 'secret' }) })
  client.recordState('secret')
  client.captureException('secret', { state: 'secret', attributes: { secret: 'secret' } })
  expect(String(fixture.records[0].body)).not.toContain('secret')
  expect(fixture.records).toHaveLength(1)
  expect(fixture.body().state.inline?.status).toBe('error')
  expect(client.stats().reentrant).toBeGreaterThan(0)
})

it('a sanitizer retaining and mutating its input/output cannot rewrite saved history', () => {
  let leaked: JsonValue = null
  const { client, body } = setup({
    sanitize: (value) => {
      leaked = value
      return value
    },
  })
  client.addBreadcrumb('one', { revision: 1 })
  ;(leaked as unknown as { revision: number }).revision = 9
  client.captureException('test')
  expect(body().history.items[0]).toMatchObject({ data: { value: { revision: 1 } } })
})

it('rate limits before readers, refills monotonically and does not merge separate occurrences', () => {
  let now = 0
  vi.spyOn(performance, 'now').mockImplementation(() => now)
  const { client, records } = setup({ rateLimit: { burst: 2, perSecond: 1 } })
  const read = vi.fn(() => ({ data: 'x'.repeat(2 ** 20) }))
  client.registerState('large', { read })
  for (let i = 0; i < 1000; i++) client.captureException('same error')
  expect(read).toHaveBeenCalledTimes(2)
  expect(records).toHaveLength(2)
  expect(records[0].attributes?.['app.debug.event.id']).not.toBe(
    records[1].attributes?.['app.debug.event.id']
  )
  now = 1000
  expect(client.captureException('same error').status).toBe('emitted')
  expect(client.stats()).toMatchObject({ attempted: 1001, emitted: 3, rateLimited: 998 })
})

it('filters before readers, isolates callback failure, and protects the original exception from mutation', () => {
  const read = vi.fn(() => null)
  const { client, body } = setup({
    filter: (error) => {
      const message = error.message
      ;(error as { message: string }).message = 'rewritten'
      if (message === 'throw') throw Error('failed filter')
      return message === 'keep'
    },
  })
  client.registerState('state', { read })
  expect(client.captureException('drop')).toEqual({ status: 'not_emitted', reason: 'filtered' })
  expect(client.captureException('throw').status).toBe('not_emitted')
  expect(read).not.toHaveBeenCalled()
  client.captureException('keep')
  expect(body().exception.message).toBe('keep')
  expect(client.stats().filtered).toBe(2)
})

it('invalid auxiliary JSON omits sections with visible diagnostics and preserves the original error', () => {
  const { client, records, body } = setup()
  const bad = { a: undefined } as unknown as Record<string, JsonValue>
  expect(
    client.captureException(new Error('original'), {
      attributes: bad as unknown as import('@opentelemetry/api-logs').LogAttributes,
      extensions: bad,
    }).status
  ).toBe('emitted')
  expect(body().exception.message).toBe('original')
  expect(body().diagnostics?.map((d) => d.code)).toEqual([
    'extensions_omitted',
    'attributes_omitted',
  ])
  expect(records[0].attributes?.['exception.message']).toBe('original')
})

it('captures aggregate causes, marks cycles/limits, and never executes cause getters', () => {
  const { client, body } = setup({ exceptionLimits: { maxDepth: 3, maxNodes: 8 } })
  const error = new AggregateError([new Error('first'), new Error('second')], 'many', {
    cause: new Error('root'),
  })
  client.captureException(error)
  expect(body().exception).toMatchObject({
    cause: { message: 'root' },
    errors: [{ message: 'first' }, { message: 'second' }],
  })
  Object.defineProperty(error, 'cause', { value: error, configurable: true })
  client.captureException(error)
  expect(body().exception.cause?.incomplete).toBe('cycle')
  const getter = vi.fn(() => {
    throw Error('do not execute')
  })
  Object.defineProperty(error, 'cause', { get: getter })
  client.captureException(error)
  expect(getter).not.toHaveBeenCalled()
  expect(body().exception.incomplete).toBe('unreadable')
  client.captureException(
    new AggregateError(
      Array.from({ length: 100 }, () => new Error('nested')),
      'many'
    )
  )
  expect(body().exception.incomplete).toBe('limit')
  expect(body().exception.errors).toHaveLength(7)
})

it('React handler captures handled error, preserves attrs/context options and separates component stack', () => {
  const { client, body } = setup()
  createReactErrorHandler(client, { extensions: { app: 'data' } })(new Error('render'), {
    componentStack: '<App>\n<Form>',
  })
  expect(body()).toMatchObject({
    exception: { handled: true },
    extensions: { app: 'data', 'backplane.react': { componentStack: '<App>\n<Form>' } },
  })
})

it('reports package scope version and returns detached statistics', () => {
  const { client, getLogger } = setup()
  expect(SDK_VERSION).toBe(JSON.parse(readFileSync('packages/errors/package.json', 'utf8')).version)
  expect(getLogger).toHaveBeenCalledWith('app-debug.browser', SDK_VERSION)
  client.captureException('one')
  const copy = client.stats()
  copy.emitted = 999
  expect(client.stats().emitted).toBe(1)
  expect(client.stats().lastCaptureMs).toBeGreaterThanOrEqual(0)
  const cleanup = vi.fn(() => {
    throw Error('cleanup')
  })
  client.onDispose(cleanup)
  client.dispose()
  client.dispose()
  expect(cleanup).toHaveBeenCalledOnce()
})

it.each([
  400, 401, 403, 404, 413, 500,
])('does not retry permanent HTTP %s failures from pinned OTel browser exporter', (status) => {
  expect(
    deliveryOutcome({
      code: 1,
      error: new Error(`Fetch request failed with non-retryable status ${status}`),
    })
  ).toBe('rejected')
})
it.each([429, 502, 503, 504])('retries HTTP %s and retains unknown failure data', (status) => {
  expect(deliveryOutcome({ code: 1, error: Object.assign(new Error(), { code: status }) })).toBe(
    'retry'
  )
})
it('uses bounded jitter and separates receiver acceptance from retryable failure', () => {
  expect(deliveryOutcome({ code: 0 })).toBe('accepted')
  expect(deliveryOutcome({ code: 1 })).toBe('retry')
  expect(retryDelay(1, () => 0)).toBe(1000)
  expect(retryDelay(1, () => 0.5)).toBe(1500)
  expect(retryDelay(100, () => 0.99)).toBeLessThanOrEqual(60_000)
})
