import { BinaryReader, WireType } from '@bufbuild/protobuf/wire';
import { resourceFromAttributes } from '@opentelemetry/resources';
import type { ReadableLogRecord } from '@opentelemetry/sdk-logs';
import { describe, expect, it, vi } from 'vitest';
import { fetchSender, retryDelay, statusOutcome } from '../packages/errors/src/delivery';
import { compareKeys, type KvKey, memoryKv, memoryLocks } from '../packages/errors/src/kv';
import { kvOutbox } from '../packages/errors/src/kv-outbox';
import { PersistentLogProcessor } from '../packages/errors/src/outbox';
import { batchRequests, encodeRecord, openRecord, recordVersion } from '../packages/errors/src/outbox-record';
import { outboxLimits, type StoredExport } from '../packages/errors/src/ports';

const record = (body: string): ReadableLogRecord => ({
  hrTime: [1, 0],
  hrTimeObserved: [1, 0],
  body,
  attributes: { 'dots.key': 'Привет' },
  droppedAttributesCount: 0,
  resource: resourceFromAttributes({ 'service.name': 'ported' }),
  instrumentationScope: { name: 'app-debug.browser' },
});

/** The bodies of an ExportLogsServiceRequest, read at the wire level: resource_logs(1) > scope_logs(2) > log_records(2) > body(5) > string_value(1). */
function bodies(request: Uint8Array): string[] {
  const out: string[] = [];
  const walk = (bytes: Uint8Array, path: number[]) => {
    const reader = new BinaryReader(bytes);
    while (reader.pos < reader.len) {
      const [field, type] = reader.tag();
      if (type !== WireType.LengthDelimited) {
        reader.skip(type);
        continue;
      }
      const next = [...path, field];
      const want = [1, 2, 2, 5, 1];
      if (next.length === want.length && next.every((f, i) => f === want[i])) out.push(reader.string());
      else if (next.every((f, i) => f === want[i])) walk(reader.bytes(), next);
      else reader.skip(type);
    }
  };
  walk(request, []);
  return out;
}

const count = (request: Uint8Array) => {
  const reader = new BinaryReader(request);
  let resourceLogs = 0;
  while (reader.pos < reader.len) {
    const [field, type] = reader.tag();
    if (field === 1) resourceLogs++;
    reader.skip(type);
  }
  return resourceLogs;
};

describe('record envelope', () => {
  it('is a version byte and a standard OTLP request', () => {
    const envelope = encodeRecord(record('{"kind":"exception"}'));
    expect(envelope[0]).toBe(recordVersion);
    const opened = openRecord(envelope);
    if (opened.kind !== 'otlp') throw new Error(opened.kind);
    expect(bodies(opened.request)).toEqual(['{"kind":"exception"}']);
  });

  it.each([
    ['empty', new Uint8Array(), 'invalid'],
    ['version 0', Uint8Array.of(0, 10, 0), 'invalid'],
    ['version alone', Uint8Array.of(1), 'invalid'],
    ['newer version', Uint8Array.of(recordVersion + 1, 10, 0), 'unknown_version'],
  ])('%s is %s', (_name, envelope, kind) => {
    expect(openRecord(envelope).kind).toBe(kind);
  });

  it('batches concatenate requests into one merged request, bounded by bytes', () => {
    const requests = ['a', 'b', 'c'].map((body) => {
      const opened = openRecord(encodeRecord(record(body)));
      if (opened.kind !== 'otlp') throw new Error(opened.kind);
      return opened.request;
    });
    const all = batchRequests(requests, 1 << 20);
    expect(all).toHaveLength(1);
    expect(all[0].count).toBe(3);
    expect(count(all[0].body)).toBe(3);
    expect(bodies(all[0].body)).toEqual(['a', 'b', 'c']);
    const two = requests[0].byteLength + requests[1].byteLength;
    expect(batchRequests(requests, two).map((b) => b.count)).toEqual([2, 1]);
    // A request larger than the bound is a body of its own.
    expect(batchRequests(requests, 1).map((b) => b.count)).toEqual([1, 1, 1]);
    expect(batchRequests([], 10)).toEqual([]);
  });
});

describe('ordered kv', () => {
  it.each<[KvKey, KvKey, number]>([
    [[1], [2], -1],
    [[2], [10], -1],
    [[10], ['1'], -1],
    [['a'], ['b'], -1],
    [['a'], ['a', 1], -1],
    [['x', 5, 'id'], ['x', 5, 'id'], 0],
  ])('%j vs %j', (a, b, want) => {
    expect(Math.sign(compareKeys(a, b))).toBe(want);
    expect(Math.sign(compareKeys(b, a))).toBe(want === 0 ? 0 : -want);
  });

  it('scans a prefix in key order and writes batches', async () => {
    const kv = memoryKv<string>();
    await kv.write([
      { put: ['o', 2, 'b'], value: 'b' },
      { put: ['o', 1, 'a'], value: 'a' },
      { put: ['other', 0], value: 'x' },
    ]);
    expect((await kv.scan(['o'])).map((e) => e.value)).toEqual(['a', 'b']);
    await kv.write([{ delete: ['o', 1, 'a'] }]);
    expect(await kv.get(['o', 1, 'a'])).toBeUndefined();
    expect(await kv.get(['o', 2, 'b'])).toBe('b');
  });

  it('memory locks run work of one name one at a time, in order, past failures', async () => {
    const locks = memoryLocks();
    const order: string[] = [];
    const slow = locks.run('n', async () => {
      await new Promise((r) => setTimeout(r, 10));
      order.push('first');
    });
    const failing = locks.run('n', () => Promise.reject(new Error('boom')));
    const third = locks.run('n', () => {
      order.push('third');
      return Promise.resolve(3);
    });
    const other = locks.run('m', async () => order.push('other'));
    await expect(failing).rejects.toThrow('boom');
    expect(await third).toBe(3);
    await Promise.all([slow, other]);
    expect(order).toEqual(['other', 'first', 'third']);
  });
});

function storage(limits = { maxBytes: 100, maxEntries: 3, maxAgeMs: 1000 }) {
  let at = 1000;
  const kv = memoryKv<StoredExport>();
  const outbox = kvOutbox({
    kv,
    prefix: ['HOST', 'telemetryOutbox', 'svc'],
    limits,
    locks: memoryLocks(),
    lock: 'l',
    now: () => at,
  });
  const row = (id: string, bytes = 10, created = at): StoredExport => ({
    id,
    payload: Uint8Array.of(1, 2),
    bytes,
    created,
    expires: created + limits.maxAgeMs,
    next: 0,
    attempts: 0,
  });
  return { kv, outbox, row, advance: (ms: number) => (at += ms), now: () => at };
}

describe('kvOutbox', () => {
  it('evicts the oldest past the budgets and refuses an entry larger than maxBytes', async () => {
    const { outbox, row, advance } = storage();
    await outbox.put(row('a'));
    advance(1);
    await outbox.put(row('b'));
    advance(1);
    await outbox.put(row('c'));
    advance(1);
    expect(await outbox.put(row('d'))).toEqual({ stored: true, evicted: 1 });
    expect(await outbox.put(row('huge', 101))).toEqual({ stored: false, evicted: 0 });
    expect(await outbox.stats()).toMatchObject({ entries: 3, bytes: 30 });
    advance(1);
    expect(await outbox.put(row('big', 80))).toEqual({ stored: true, evicted: 1 });
  });

  it('leases to one owner, retries with backoff, ignores a foreign finish, expires', async () => {
    const { outbox, row, advance } = storage();
    await outbox.put(row('a'));
    const first = await outbox.claim('one', false);
    expect(first.entries.map((e) => e.id)).toEqual(['a']);
    expect((await outbox.claim('two', true)).entries).toEqual([]);
    await outbox.finish(first.entries, 'two', true);
    expect((await outbox.stats()).entries).toBe(1);
    await outbox.finish(first.entries, 'one', false);
    expect((await outbox.claim('two', false)).entries).toEqual([]);
    const forced = await outbox.claim('two', true);
    expect(forced.entries[0]).toMatchObject({ id: 'a', attempts: 1 });
    advance(30_001);
    expect((await outbox.claim('three', true)).expired).toBe(1);
  });

  it('a lease ends after 30 s; corrupt rows are dropped and counted', async () => {
    const { kv, outbox, row, advance } = storage({ maxBytes: 100, maxEntries: 3, maxAgeMs: 120_000 });
    await outbox.put(row('a'));
    await outbox.claim('one', true);
    advance(30_001);
    expect((await outbox.claim('two', true)).entries.map((e) => e.id)).toEqual(['a']);
    await kv.write([{ put: ['HOST', 'telemetryOutbox', 'svc', 0, 'bad'], value: { ...row('bad'), bytes: -1 } }]);
    expect((await outbox.claim('three', true)).expired).toBe(1);
  });

  it('clears its own rows only and refuses work after close', async () => {
    const { kv, outbox, row } = storage();
    await kv.write([{ put: ['HOST', 'other'], value: row('foreign') }]);
    await outbox.put(row('a'));
    await outbox.clear();
    expect((await outbox.stats()).entries).toBe(0);
    expect(await kv.get(['HOST', 'other'])).toBeDefined();
    outbox.close();
    await expect(outbox.stats()).rejects.toThrow('outbox_closed');
  });
});

describe('delivery', () => {
  it.each([
    [200, 'accepted'],
    [204, 'accepted'],
    [400, 'rejected'],
    [413, 'rejected'],
    [500, 'rejected'],
    [408, 'retry'],
    [429, 'retry'],
    [502, 'retry'],
    [503, 'retry'],
    [504, 'retry'],
  ])('status %i is %s', (status, outcome) => {
    expect(statusOutcome(status)).toBe(outcome);
  });

  it('retry delay grows to a minute with jitter', () => {
    expect(retryDelay(1, () => 0)).toBe(1000);
    expect(retryDelay(20, () => 1)).toBe(60_000);
  });

  it('fetchSender posts protobuf, keeps small urgent bodies alive, retries on network failure', async () => {
    const calls: RequestInit[] = [];
    const ok = fetchSender({
      url: 'https://otlp.example/v1/logs',
      headers: { Authorization: 'Bearer t' },
      fetch: (_url, init) => {
        if (init) calls.push(init);
        return Promise.resolve(new Response(null, { status: 200 }));
      },
    });
    expect(await ok.send(Uint8Array.of(1), true)).toBe('accepted');
    expect(await ok.send(new Uint8Array(70_000), true)).toBe('accepted');
    expect(calls.map((c) => c.keepalive)).toEqual([true, false]);
    expect(calls[0].headers).toMatchObject({ 'Content-Type': 'application/x-protobuf', Authorization: 'Bearer t' });
    const down = fetchSender({ url: 'x', fetch: () => Promise.reject(new TypeError('offline')) });
    expect(await down.send(Uint8Array.of(1), false)).toBe('retry');
  });
});

describe('PersistentLogProcessor', () => {
  const runtime = (ids = { n: 0 }) => ({ now: () => 1000, monotonic: () => 0, randomId: () => `id-${++ids.n}` });

  it('runs on ports alone, sends merged batches bounded by bytes, drops unknown versions', async () => {
    expect(typeof window).toBe('undefined');
    vi.stubGlobal('crypto', undefined);
    vi.stubGlobal('location', undefined);
    try {
      const kv = memoryKv<StoredExport>();
      const sent: Uint8Array[] = [];
      const diagnostics: string[] = [];
      const one = openRecord(encodeRecord(record('x')));
      if (one.kind !== 'otlp') throw new Error(one.kind);
      const processor = new PersistentLogProcessor(
        {
          send: (body) => {
            sent.push(body);
            return Promise.resolve('accepted');
          },
        },
        {
          storage: kvOutbox({
            kv,
            prefix: ['p'],
            limits: { maxBytes: 1 << 20, maxEntries: 100, maxAgeMs: 60_000 },
            locks: memoryLocks(),
            lock: 'p',
            now: () => 1000,
          }),
          // Two records per body.
          maxBatchBytes: one.request.byteLength * 2,
        },
        runtime(),
        (code) => diagnostics.push(code),
      );
      for (const body of ['x', 'y', 'z']) processor.onEmit(record(body));
      await kv.write([
        {
          put: ['p', 999, 'future'],
          value: {
            id: 'future',
            payload: Uint8Array.of(9, 1),
            bytes: 2,
            created: 999,
            expires: 99_999,
            next: 0,
            attempts: 0,
          },
        },
      ]);
      await processor.flushStorage();
      await processor.forceFlush();
      expect(sent.flatMap(bodies).sort()).toEqual(['x', 'y', 'z']);
      expect(sent.every((body) => body.byteLength <= one.request.byteLength * 2)).toBe(true);
      expect(diagnostics).toContain('outbox_unknown_version');
      expect(await processor.stats()).toMatchObject({
        entries: 0,
        persisted: 3,
        accepted: 3,
        unknownVersion: 1,
        invalid: 0,
      });
      await processor.shutdown();
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('keeps every record on a retry and sends it on the next drain', async () => {
    const kv = memoryKv<StoredExport>();
    let up = false;
    const sent: string[] = [];
    const processor = new PersistentLogProcessor(
      {
        send: (body) => {
          if (!up) return Promise.resolve('retry');
          sent.push(...bodies(body));
          return Promise.resolve('accepted');
        },
      },
      {
        storage: kvOutbox({
          kv,
          prefix: ['p'],
          limits: { maxBytes: 1 << 20, maxEntries: 100, maxAgeMs: 60_000 },
          locks: memoryLocks(),
          lock: 'p',
          now: () => 1000,
        }),
        maxBatchBytes: 1,
      },
      runtime(),
      () => {},
    );
    processor.onEmit(record('a'));
    processor.onEmit(record('b'));
    await processor.flushStorage();
    await processor.forceFlush();
    expect((await processor.stats()).entries).toBe(2);
    up = true;
    await processor.forceFlush();
    expect(sent.sort()).toEqual(['a', 'b']);
    expect((await processor.stats()).entries).toBe(0);
    await processor.shutdown();
  });

  it('refuses a non-positive batch bound', () => {
    expect(
      () =>
        new PersistentLogProcessor(
          { send: () => Promise.resolve('accepted') },
          {
            storage: kvOutbox({
              kv: memoryKv(),
              prefix: [],
              limits: { maxBytes: 1, maxEntries: 1, maxAgeMs: 1 },
              locks: memoryLocks(),
              lock: 'l',
              now: () => 0,
            }),
            maxBatchBytes: 0,
          },
          runtime(),
          () => {},
        ),
    ).toThrow('invalid_outbox_options');
  });
});

it('outbox limits require an explicit namespace and finite positive budgets', () => {
  expect(outboxLimits({ name: 'app-account-42' })).toEqual({
    name: 'app-account-42',
    maxBytes: 10 * 1024 * 1024,
    maxEntries: 1000,
    maxAgeMs: 86400000,
  });
  for (const options of [
    { name: '' },
    { name: 'account/a' },
    { name: 'a', maxEntries: 0 },
    { name: 'a', maxBytes: Infinity },
    { name: 'a', maxAgeMs: -1 },
  ]) {
    expect(() => outboxLimits(options)).toThrow();
  }
});
