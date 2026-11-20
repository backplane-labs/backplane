import { createTraceState, ROOT_CONTEXT, trace } from '@opentelemetry/api';
import { createBrowserClient } from '../../packages/errors/src/browser';
import type { OutboxOptions } from '../../packages/errors/src/ports';

export function outboxFixture(name: string, url: string, limits: Partial<OutboxOptions> = {}) {
  const diagnostics: string[] = [];
  const client = createBrowserClient({
    url,
    resource: { 'service.name': name },
    outbox: { name, ...limits },
    onDiagnostic: (diagnostic) => diagnostics.push(diagnostic.code),
  });
  return {
    client,
    diagnostics,
    capture(size = 0) {
      const state = { revision: 1, values: [null, false, 0, '', 'Привет'], large: 'x'.repeat(size) };
      client.registerState('editor', { read: () => state });
      client.addBreadcrumb('clicked', { 'key.with.dots': '<script>throw new Error()</script>' });
      client.recordState('editor');
      state.revision = 2;
      const error = new Error('durable browser error');
      error.stack = 'original stack\n  <script>untrusted</script>';
      const result = client.captureException(error, {
        state: { inline: false },
        context: trace.setSpanContext(ROOT_CONTEXT, {
          traceId: 'a'.repeat(32),
          spanId: 'b'.repeat(16),
          traceFlags: 0,
          traceState: createTraceState('vendor=value'),
        }),
      });
      state.revision = 3;
      return result;
    },
  };
}

// Sender fixtures only: storage is native IndexedDB, transport results are deterministic.
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
  ]);
  const url = 'http://localhost:14318/v1/logs';
  const ports = (options: OutboxOptions) => ({
    storage: indexedDbOutbox(url, options),
    triggers: browserTriggers(),
  });
  const diagnostics: string[] = [];
  const bodies: Uint8Array[] = [];
  let succeed = false,
    reentrant = false;
  let client: ReturnType<typeof createClient> | undefined;
  const sender = {
    send: (body: Uint8Array) => {
      bodies.push(body);
      return Promise.resolve(succeed ? ('accepted' as const) : ('retry' as const));
    },
  };
  const text = (body: Uint8Array) => new TextDecoder().decode(body);
  const diagnostic = (code: string) => {
    diagnostics.push(code);
    if (reentrant) client?.captureException('diagnostic recursion');
  };
  const name = `processor-${crypto.randomUUID()}`;
  const processor = new PersistentLogProcessor(sender, ports({ name }), browserRuntime, diagnostic);
  const provider = new LoggerProvider({ processors: [processor] });
  client = createClient({ loggerProvider: provider, runtime: browserRuntime });
  client.captureException('retry original');
  await processor.flushStorage();
  await processor.forceFlush();
  const retryBefore = await processor.stats();
  succeed = true;
  await processor.forceFlush();
  const retryAfter = await processor.stats();
  const bodyStable = bodies.length >= 2 && bodies.every((body) => text(body) === text(bodies[0]));
  // Rows another build wrote: an invalid envelope and a newer version. Both are dropped and counted.
  const databaseInfo = (await indexedDB.databases()).find((database) => database.name?.includes(name));
  if (!databaseInfo?.name) throw new Error('Missing native IDB');
  const databaseName = databaseInfo.name;
  const database = await new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open(databaseName);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
  await new Promise<void>((resolve, reject) => {
    const tx = database.transaction('kv', 'readwrite');
    const row = (id: string, payload: Uint8Array) => ({
      id,
      payload,
      bytes: payload.byteLength,
      created: Date.now(),
      expires: Date.now() + 60000,
      next: 0,
      attempts: 0,
    });
    tx.objectStore('kv').put(row('corrupt', Uint8Array.of(0)), [Date.now(), 'corrupt']);
    tx.objectStore('kv').put(row('future', Uint8Array.of(200, 10, 0)), [Date.now(), 'future']);
    tx.oncomplete = () => resolve();
    tx.onabort = () => reject(tx.error);
  });
  database.close();
  await processor.forceFlush();
  const dropped = await processor.stats();
  client.dispose();
  await provider.shutdown();

  const oversized = new PersistentLogProcessor(
    sender,
    ports({ name: `${name}-small`, maxBytes: 100 }),
    browserRuntime,
    diagnostic,
  );
  const smallProvider = new LoggerProvider({ processors: [oversized] });
  client = createClient({ loggerProvider: smallProvider, runtime: browserRuntime });
  reentrant = true;
  const before = bodies.length;
  client.captureException('oversized', { state: 'x'.repeat(100_000) });
  await oversized.forceFlush();
  await new Promise((resolve) => setTimeout(resolve, 50));
  const fallbackBodyLength = bodies.at(-1)?.byteLength ?? 0;
  const fallbackCount = bodies.length - before;
  client.dispose();
  await smallProvider.shutdown();

  const descriptor = Object.getOwnPropertyDescriptor(window, 'indexedDB');
  Object.defineProperty(window, 'indexedDB', { configurable: true, value: undefined });
  let storageFlushFailed = false;
  const unavailable = new PersistentLogProcessor(
    sender,
    ports({ name: `${name}-blocked` }),
    browserRuntime,
    diagnostic,
  );
  const blockedProvider = new LoggerProvider({ processors: [unavailable] });
  client = createClient({ loggerProvider: blockedProvider, runtime: browserRuntime });
  client.captureException('storage blocked');
  try {
    await unavailable.flushStorage();
  } catch {
    storageFlushFailed = true;
  }
  unavailable.stop();
  client.dispose();
  try {
    await blockedProvider.shutdown();
  } catch {
    /* Expected failed flush. */
  }
  if (descriptor) Object.defineProperty(window, 'indexedDB', descriptor);
  return {
    retryBefore,
    retryAfter,
    bodyStable,
    dropped: { invalid: dropped.invalid, unknownVersion: dropped.unknownVersion },
    diagnostics,
    storageFlushFailed,
    fallbackBodyLength,
    fallbackCount,
    reentrantEmits: bodies.filter((body) => text(body).includes('diagnostic recursion')).length,
  };
}
