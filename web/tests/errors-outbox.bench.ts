import { resourceFromAttributes } from '@opentelemetry/resources';
import type { ReadableLogRecord } from '@opentelemetry/sdk-logs';
import { test } from 'vitest';
import { memoryKv, memoryLocks } from '../packages/errors/src/kv';
import { kvOutbox } from '../packages/errors/src/kv-outbox';
import { batchRequests, encodeRecord, openRecord } from '../packages/errors/src/outbox-record';
import type { StoredExport } from '../packages/errors/src/ports';

const record: ReadableLogRecord = {
  hrTime: [1, 0],
  hrTimeObserved: [1, 0],
  body: JSON.stringify({ kind: 'exception', stack: 'x'.repeat(4000) }),
  attributes: { 'exception.type': 'Error' },
  droppedAttributesCount: 0,
  resource: resourceFromAttributes({ 'service.name': 'bench' }),
  instrumentationScope: { name: 'app-debug.browser' },
};
const envelope = encodeRecord(record);
const opened = openRecord(envelope);
const requests = opened.kind === 'otlp' ? Array.from({ length: 32 }, () => opened.request) : [];

test('outbox record', async ({ bench }) => {
  await bench.compare(
    bench('encodeRecord', () => {
      encodeRecord(record);
    }),
    bench('batchRequests 32 x 4 KiB into 64 KiB bodies', () => {
      batchRequests(requests, 64 * 1024);
    }),
  );
});

test('kvOutbox over memoryKv', async ({ bench }) => {
  let n = 0;
  const outbox = kvOutbox({
    kv: memoryKv<StoredExport>(),
    prefix: ['HOST', 'telemetryOutbox', 'bench'],
    limits: { maxBytes: 10 * 1024 * 1024, maxEntries: 1000, maxAgeMs: 86_400_000 },
    locks: memoryLocks(),
    lock: 'bench',
    now: () => 1000,
  });
  await bench('put, claim and finish one record', async () => {
    const id = `r${++n}`;
    await outbox.put({
      id,
      payload: envelope,
      bytes: envelope.byteLength,
      created: n,
      expires: 86_400_000,
      next: 0,
      attempts: 0,
    });
    const claimed = await outbox.claim('owner', true);
    await outbox.finish(claimed.entries.slice(0, 1), 'owner', true);
  }).run();
});
