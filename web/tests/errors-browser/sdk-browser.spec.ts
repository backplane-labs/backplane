import { resolve } from 'node:path'
import { writeFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'

const root = resolve(import.meta.dirname, '../..')
const sdk = `/@fs/${root}/packages/errors/src`

test('local snapshot is detached and matches capture with native browser clocks frozen', async ({
  page,
}) => {
  await page.goto('http://127.0.0.1:4181')
  const result = await page.evaluate(async (base) => {
    const { createClient, redactKeys } = await import(`${base}/index.ts`)
    const { browserRuntime } = await import(`${base}/browser.ts`)
    const records: { body: string }[] = []
    const client = createClient({
      runtime: browserRuntime,
      loggerProvider: { getLogger: () => ({ emit: (r: { body: string }) => records.push(r) }) },
      sanitize: redactKeys(['password']),
    })
    const originalDate = Date.now
    Object.defineProperty(performance, 'now', { value: () => 100, configurable: true })
    Date.now = () => 1_750_000_000_000
    try {
      client.registerState('editor', { read: () => ({ password: 'SECRET_BROWSER', count: 1 }) })
      client.recordState('editor')
      client.addBreadcrumb('log.editor', { password: 'SECRET_LOG', message: 'saved' })
      const local = client.snapshot()
      const clone = structuredClone(local)
      const beforeEmit = records.length
      client.captureException('compare')
      const envelope = JSON.parse(records[0].body)
      Object.assign(local.value.history.items[0], { snapshot: {} })
      const later = client.snapshot()
      return { clone, beforeEmit, envelope, later, stats: client.stats() }
    } finally {
      Date.now = originalDate
      Reflect.deleteProperty(performance, 'now')
      client.dispose()
    }
  }, sdk)
  expect(result.beforeEmit).toBe(0)
  expect(result.clone.status).toBe('ok')
  expect(result.clone.value.state.sources).toEqual(result.envelope.state.sources)
  expect(result.clone.value.history.items).toEqual(result.envelope.history.items)
  expect(result.later.value.history.items).toEqual(result.envelope.history.items)
  expect(JSON.stringify(result.clone)).not.toContain('SECRET_')
  expect(result.stats).toMatchObject({ snapshots: 2, attempted: 1, emitted: 1 })
})

test('local snapshot leaves native IndexedDB outbox and transport untouched', async ({ page }) => {
  await page.goto('http://127.0.0.1:4181')
  let requests = 0
  await page.route('**/snapshot-only/v1/logs', (route) => {
    requests++
    return route.fulfill({ status: 200 })
  })
  const result = await page.evaluate(async (base) => {
    const { createBrowserClient } = await import(`${base}/browser.ts`)
    let filters = 0
    const client = createBrowserClient({
      url: `${location.origin}/snapshot-only/v1/logs`,
      outbox: { name: `snapshot-${crypto.randomUUID()}` },
      filter: () => {
        filters++
        return false
      },
      rateLimit: { burst: 1, perSecond: 0.001 },
    })
    client.registerState('editor', { read: () => ({ ready: true }) })
    client.addBreadcrumb('log.app', { message: 'user reporting a bug' })
    await client.outbox.flushStorage()
    const before = await client.outbox.stats()
    const beforeClient = client.stats()
    const values = [client.snapshot(), client.snapshot({ maxBytes: 1 })]
    await client.outbox.flushStorage()
    await client.flush()
    const after = await client.outbox.stats()
    const afterClient = client.stats()
    await client.shutdown()
    return { before, after, beforeClient, afterClient, filters, values }
  }, sdk)
  expect(requests).toBe(0)
  expect(result.filters).toBe(0)
  expect(result.after).toEqual(result.before)
  expect(result.afterClient).toEqual({ ...result.beforeClient, snapshots: 2 })
  expect(result.values.every((r: { status: string }) => r.status === 'ok')).toBe(true)
})

test('native fetch/XHR/navigation: safe metadata, exclusions, failures, ownership and disposal', async ({
  page,
}) => {
  await page.goto('http://127.0.0.1:4181')
  await page.route('**/synthetic-api**', (route) =>
    route.fulfill({ status: 503, body: 'synthetic body must not be recorded' })
  )
  await page.route('**/v1/logs', (route) => route.fulfill({ status: 200 }))
  await page.route('**/network-failure', (route) => route.abort())
  const result = await page.evaluate(async (base) => {
    const { createClient } = await import(`${base}/index.ts`)
    const { browserRuntime, instrumentBrowser } = await import(`${base}/browser.ts`)
    const records: { body: string }[] = []
    const client = createClient({
      runtime: browserRuntime,
      loggerProvider: {
        getLogger: () => ({ emit: (record: { body: string }) => records.push(record) }),
      },
    })
    const original = fetch,
      originalOpen = XMLHttpRequest.prototype.open,
      originalPush = history.pushState
    const dispose = instrumentBrowser(client, { fetch: true, xhr: true, navigation: true })
    let duplicateBlocked = false,
      failed = false
    try {
      instrumentBrowser(client, { fetch: true })
    } catch {
      duplicateBlocked = true
    }
    const response = await fetch('/synthetic-api?token=SECRET_FETCH', {
      headers: { Authorization: 'SECRET_HEADER' },
    })
    const text = await response.text()
    await fetch('/v1/logs', { method: 'POST' })
    try {
      await fetch('/network-failure')
    } catch {
      failed = true
    }
    await new Promise<void>((resolve) => {
      const xhr = new XMLHttpRequest()
      xhr.open('GET', '/synthetic-api?token=SECRET_XHR')
      xhr.onloadend = () => resolve()
      xhr.send()
    })
    history.pushState({ secret: 'SECRET_HISTORY' }, '', '/route?token=SECRET_ROUTE#SECRET_HASH')
    client.captureException('after requests')
    const first = JSON.parse(records[0].body)
    dispose()
    dispose()
    await fetch('/synthetic-api')
    client.captureException('after dispose')
    const after = JSON.parse(records[1].body)
    const restored =
      fetch === original &&
      XMLHttpRequest.prototype.open === originalOpen &&
      history.pushState === originalPush
    instrumentBrowser(client, { fetch: true })
    client.dispose()
    return {
      first,
      after,
      duplicateBlocked,
      failed,
      status: response.status,
      text,
      restored,
      clientDisposes: fetch === original,
    }
  }, sdk)
  expect(result.status).toBe(503)
  expect(result.text).toBe('synthetic body must not be recorded')
  expect(result.failed).toBe(true)
  expect(result.duplicateBlocked).toBe(true)
  expect(result.restored).toBe(true)
  expect(result.clientDisposes).toBe(true)
  expect(result.first.history.items.map((item: { name: string }) => item.name)).toEqual([
    'http.fetch',
    'http.fetch',
    'http.xhr',
    'navigation',
  ])
  expect(JSON.stringify(result.first)).not.toContain('SECRET_')
  expect(JSON.stringify(result.first)).not.toContain('synthetic body')
  expect(result.after.history.items).toEqual(result.first.history.items)
})

test('native IndexedDB stores sanitized data only and enforces whole-record budgets under a burst', async ({
  page,
}) => {
  await page.goto('http://127.0.0.1:4181')
  await page.route('**/blocked/v1/logs', (route) => route.abort())
  const result = await page.evaluate(async (base) => {
    const { createBrowserClient } = await import(`${base}/browser.ts`)
    const { redactKeys } = await import(`${base}/index.ts`)
    const name = `privacy-${crypto.randomUUID()}`
    const client = createBrowserClient({
      url: `${location.origin}/blocked/v1/logs`,
      sanitize: redactKeys(['password']),
      outbox: { name, maxEntries: 5, maxBytes: 40_000 },
    })
    client.registerState('form', {
      read: () => ({ password: 'SECRET_IDB', value: 'x'.repeat(1000) }),
    })
    client.recordState('form')
    for (let i = 0; i < 20; i++) {
      client.captureException('burst', { state: { password: 'SECRET_INLINE', i } })
      await client.outbox.flushStorage()
    }
    client.dispose()
    const stats = await client.outbox.stats()
    const databaseName = (await indexedDB.databases()).find((db) => db.name?.includes(name))?.name
    if (!databaseName) throw new Error('no database')
    const database = await new Promise<IDBDatabase>((resolve, reject) => {
      const r = indexedDB.open(databaseName)
      r.onsuccess = () => resolve(r.result)
      r.onerror = () => reject(r.error)
    })
    const rows = await new Promise<unknown[]>((resolve, reject) => {
      const tx = database.transaction('records'),
        request = tx.objectStore('records').getAll()
      request.onsuccess = () => resolve(request.result)
      request.onerror = () => reject(request.error)
    })
    database.close()
    await client.outbox.clear()
    await client.shutdown()
    return { stats, rows }
  }, sdk)
  expect(result.stats.entries).toBeLessThanOrEqual(5)
  expect(result.stats.bytes).toBeLessThanOrEqual(40_000)
  expect(result.stats.evicted).toBeGreaterThan(0)
  expect(result.stats.persisted).toBe(20)
  expect(result.stats.oldestAgeMs).toBeGreaterThanOrEqual(0)
  expect(JSON.stringify(result.rows)).not.toContain('SECRET_')
  expect(JSON.stringify(result.rows)).toContain('[REDACTED]')
})

test('real protobuf exporter classifies HTTP rejection and recovers after a network outage', async ({
  page,
}) => {
  await page.goto('http://127.0.0.1:4181')
  let rejectedRequests = 0,
    acceptedRequests = 0,
    accept = false
  await page.route('**/permanent/v1/logs', async (route) => {
    expect(route.request().headers()['content-type']).toBe('application/x-protobuf')
    expect(route.request().postDataBuffer()?.length).toBeGreaterThan(0)
    rejectedRequests++
    await route.fulfill({ status: 400 })
  })
  await page.route('**/retry/v1/logs', async (route) => {
    if (accept) {
      acceptedRequests++
      await route.fulfill({ status: 200 })
    } else await route.abort()
  })
  const rejected = await page.evaluate(async (base) => {
    const { createBrowserClient } = await import(`${base}/browser.ts`)
    const client = createBrowserClient({
      url: `${location.origin}/permanent/v1/logs`,
      outbox: { name: crypto.randomUUID() },
    })
    client.captureException('bad request')
    await client.flush()
    await client.flush()
    const stats = await client.outbox.stats()
    await client.shutdown()
    return stats
  }, sdk)
  expect(rejectedRequests).toBe(1)
  expect(rejected.rejected).toBe(1)
  expect(rejected.entries).toBe(0)
  const before = await page.evaluate(async (base) => {
    const { createBrowserClient } = await import(`${base}/browser.ts`)
    const client = createBrowserClient({
      url: `${location.origin}/retry/v1/logs`,
      outbox: { name: crypto.randomUUID() },
    })
    ;(window as unknown as { retryClient: typeof client }).retryClient = client
    client.captureException('retry later')
    await client.outbox.flushStorage()
    await client.flush()
    return client.outbox.stats()
  }, sdk)
  expect(before.entries).toBe(1)
  expect(before.retried).toBeGreaterThan(0)
  accept = true
  const after = await page.evaluate(async () => {
    const client = (
      window as unknown as {
        retryClient: {
          flush(): Promise<void>
          shutdown(): Promise<void>
          outbox: { stats(): Promise<{ entries: number; accepted: number }> }
        }
      }
    ).retryClient
    await client.flush()
    const stats = await client.outbox.stats()
    await client.shutdown()
    return stats
  })
  expect(after.entries).toBe(0)
  expect(after.accepted).toBe(1)
  expect(acceptedRequests).toBe(1)
})

test('large captures and an error storm: measured latency, intact payload and early admission control', async ({
  page,
}, testInfo) => {
  await page.goto('http://127.0.0.1:4181')
  const result = await page.evaluate(async (base) => {
    const { createClient } = await import(`${base}/index.ts`)
    const { browserRuntime: runtime } = await import(`${base}/browser.ts`)
    let last = '',
      reads = 0
    const provider = {
      getLogger: () => ({
        emit: (record: { body: string }) => {
          last = record.body
        },
      }),
    }
    const client = createClient({ loggerProvider: provider, runtime })
    const measurements = []
    for (const bytes of [10_240, 1_048_576]) {
      const values: number[] = []
      const unregister = client.registerState('large', {
        read: () => ({ payload: 'x'.repeat(bytes) }),
      })
      for (let i = 0; i < 20; i++) {
        client.captureException('measured')
        values.push(client.stats().lastCaptureMs)
      }
      values.sort((a, b) => a - b)
      measurements.push({
        bytes,
        p95Ms: values[18],
        maxMs: values[19],
        retainedBytes: JSON.parse(last).state.sources[0].value.payload.length,
      })
      unregister()
    }
    client.dispose()
    const limited = createClient({
      loggerProvider: provider,
      runtime,
      rateLimit: { burst: 2, perSecond: 0.001 },
    })
    limited.registerState('expensive', {
      read: () => {
        reads++
        return { value: 'x'.repeat(1_048_576) }
      },
    })
    const started = performance.now()
    for (let i = 0; i < 10_000; i++) limited.captureException('storm')
    const stormMs = performance.now() - started,
      stats = limited.stats()
    limited.dispose()
    return { measurements, reads, stats, stormMs }
  }, sdk)
  const output = testInfo.outputPath('sdk-performance.json')
  await writeFile(output, JSON.stringify(result, null, 2))
  await testInfo.attach('sdk-performance.json', { path: output, contentType: 'application/json' })
  expect(result.reads).toBe(2)
  expect(result.stats.rateLimited).toBe(9998)
  expect(result.stormMs).toBeLessThan(10_000)
  for (const measurement of result.measurements) {
    expect(measurement.retainedBytes).toBe(measurement.bytes)
    expect(measurement.maxMs).toBeLessThan(5000)
  }
})

test('React rendering failure reaches the SDK once with its component stack', async ({ page }) => {
  await page.goto('http://127.0.0.1:4181')
  const result = await page.evaluate(
    async (path) => (await import(path)).reactFixture(),
    `/@fs/${root}/tests/errors-browser/react-fixture.ts`
  )
  expect(result.fallback).toBe('Fallback visible')
  expect(result.records).toHaveLength(1)
  const body = JSON.parse(result.records[0].body)
  expect(body.exception).toMatchObject({ message: 'Real React render failure', handled: true })
  expect(body.extensions['backplane.react'].componentStack).toContain('Broken')
})
