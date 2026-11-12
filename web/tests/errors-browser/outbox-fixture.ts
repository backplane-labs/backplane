import { ROOT_CONTEXT, trace, createTraceState } from '@opentelemetry/api'
import { createBrowserClient } from '../../packages/errors/src/browser'
import type { OutboxOptions } from '../../packages/errors/src/ports'

export function outboxFixture(name: string, url: string, limits: Partial<OutboxOptions> = {}) {
  const diagnostics: string[] = []
  const client = createBrowserClient({
    url,
    resource: { 'service.name': name },
    outbox: { name, ...limits },
    onDiagnostic: (diagnostic) => diagnostics.push(diagnostic.code),
  })
  return {
    client,
    diagnostics,
    capture(size = 0) {
      const state = { revision: 1, values: [null, false, 0, '', 'Привет'], large: 'x'.repeat(size) }
      client.registerState('editor', { read: () => state })
      client.addBreadcrumb('clicked', { 'key.with.dots': '<script>throw new Error()</script>' })
      client.recordState('editor')
      state.revision = 2
      const error = new Error('durable browser error')
      error.stack = 'original stack\n  <script>untrusted</script>'
      const result = client.captureException(error, {
        state: { inline: false },
        context: trace.setSpanContext(ROOT_CONTEXT, {
          traceId: 'a'.repeat(32),
          spanId: 'b'.repeat(16),
          traceFlags: 0,
          traceState: createTraceState('vendor=value'),
        }),
      })
      state.revision = 3
      return result
    },
  }
}

// Exporter fixtures only: failure paths use native IndexedDB, with deterministic transport results.
export async function processorFixture() {
  const [
    { PersistentLogProcessor },
    { LoggerProvider },
    { createClient },
    { browserRuntime, browserTriggers, indexedDbOutbox },
  ] = await Promise.all([
    import('../../packages/errors/src/outbox'),
    import('@opentelemetry/sdk-logs'),
    import('../../packages/errors/src/index'),
    import('../../packages/errors/src/browser'),
  ])
  const url = 'http://localhost:14318/v1/logs'
  const ports = (options: OutboxOptions) => ({
    storage: indexedDbOutbox(url, options),
    triggers: browserTriggers(),
  })
  const diagnostics: string[] = []
  const bodies: string[] = []
  let succeed = false,
    reentrant = false
  let client: ReturnType<typeof createClient> | undefined
  const exporter = {
    export: (
      records: import('@opentelemetry/sdk-logs').ReadableLogRecord[],
      callback: (result: { code: number }) => void
    ) => {
      bodies.push(...records.map((record) => String(record.body)))
      callback({ code: succeed ? 0 : 1 })
    },
    forceFlush: async () => {},
    shutdown: async () => {},
  }
  const diagnostic = (code: string) => {
    diagnostics.push(code)
    if (reentrant) client?.captureException('diagnostic recursion')
  }
  const name = `processor-${crypto.randomUUID()}`
  const processor = new PersistentLogProcessor(exporter, ports({ name }), browserRuntime, diagnostic)
  const provider = new LoggerProvider({ processors: [processor] })
  client = createClient({ loggerProvider: provider, runtime: browserRuntime })
  client.captureException('retry original')
  await processor.flushStorage()
  await processor.forceFlush()
  const retryBefore = await processor.stats()
  succeed = true
  await processor.forceFlush()
  const retryAfter = await processor.stats()
  const bodyStable = bodies.length >= 2 && new Set(bodies).size === 1
  // Corrupt on-disk DTO: drop with diagnosis, never export invented fields.
  const databaseInfo = (await indexedDB.databases()).find((database) =>
    database.name?.includes(name)
  )
  if (!databaseInfo?.name) throw new Error('Missing native IDB')
  const databaseName = databaseInfo.name
  const database = await new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open(databaseName)
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error)
  })
  await new Promise<void>((resolve, reject) => {
    const tx = database.transaction('records', 'readwrite')
    tx.objectStore('records').put({
      id: 'corrupt',
      payload: '{',
      bytes: 1,
      created: Date.now(),
      expires: Date.now() + 60000,
      next: 0,
      attempts: 0,
    })
    tx.oncomplete = () => resolve()
    tx.onabort = () => reject(tx.error)
  })
  database.close()
  await processor.forceFlush()
  client.dispose()
  await provider.shutdown()

  const oversized = new PersistentLogProcessor(
    exporter,
    ports({ name: `${name}-small`, maxBytes: 100 }),
    browserRuntime,
    diagnostic
  )
  const smallProvider = new LoggerProvider({ processors: [oversized] })
  client = createClient({ loggerProvider: smallProvider, runtime: browserRuntime })
  reentrant = true
  client.captureException('oversized', { state: 'x'.repeat(100_000) })
  await oversized.forceFlush()
  const fallbackBodyLength = bodies.at(-1)?.length ?? 0
  client.dispose()
  await smallProvider.shutdown()

  const descriptor = Object.getOwnPropertyDescriptor(window, 'indexedDB')
  Object.defineProperty(window, 'indexedDB', { configurable: true, value: undefined })
  let storageFlushFailed = false
  const before = bodies.length
  const unavailable = new PersistentLogProcessor(
    exporter,
    ports({ name: `${name}-blocked` }),
    browserRuntime,
    diagnostic
  )
  const blockedProvider = new LoggerProvider({ processors: [unavailable] })
  client = createClient({ loggerProvider: blockedProvider, runtime: browserRuntime })
  client.captureException('storage blocked')
  try {
    await unavailable.flushStorage()
  } catch {
    storageFlushFailed = true
  }
  unavailable.stop()
  client.dispose()
  try {
    await blockedProvider.shutdown()
  } catch {
    /* Expected failed flush, exporter still closes. */
  }
  if (descriptor) Object.defineProperty(window, 'indexedDB', descriptor)
  return {
    retryBefore,
    retryAfter,
    bodyStable,
    diagnostics,
    storageFlushFailed,
    fallbackBodyLength,
    fallbackCount: bodies.length - before,
    reentrantEmits: bodies.filter(
      (body) => JSON.parse(body).exception.message === 'diagnostic recursion'
    ).length,
  }
}
