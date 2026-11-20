import { resolve } from 'node:path';
import { expect, test } from '@playwright/test';

const root = resolve(import.meta.dirname, '../..');

test('kvOutbox over native IndexedDB and Web Locks: budgets, expiry, leases, clear, isolation', async ({ page }) => {
  await page.goto('http://127.0.0.1:4181');
  const result = await page.evaluate(
    async ([kvPath, outboxPath, browserPath]) => {
      const [{ IndexedDbKv }, { kvOutbox }, { webLocks }] = await Promise.all([
        import(kvPath),
        import(outboxPath),
        import(browserPath),
      ]);
      const name = `storage-${crypto.randomUUID()}`;
      const limits = { maxBytes: 10, maxEntries: 2, maxAgeMs: 3600_000 };
      // Two tabs: their own handles on one database, one lock name.
      const tab = (db: string, prefix: (string | number)[]) =>
        kvOutbox({ kv: new IndexedDbKv(db), prefix, limits, locks: webLocks(), lock: db, now: () => Date.now() });
      const first = tab(name, ['HOST', 'telemetryOutbox', 'svc']),
        second = tab(name, ['HOST', 'telemetryOutbox', 'svc']);
      const neighbour = tab(name, ['HOST', 'telemetryOutbox', 'other']);
      const other = tab(`${name}-other`, ['HOST', 'telemetryOutbox', 'svc']);
      let created = Date.now();
      const row = (id: string, bytes = 4) => ({
        id,
        bytes,
        payload: Uint8Array.of(1, 10, 0),
        created: created++,
        expires: Date.now() + 3600_000,
        next: 0,
        attempts: 0,
      });
      await first.put(row('a'));
      await first.put(row('b'));
      const evicted = await first.put(row('c'));
      const oversized = await first.put(row('oversized', 11));
      await neighbour.put(row('n'));
      const stats = await first.stats();
      // Racing claims from both tabs: no entry is leased twice.
      const [one, two] = await Promise.all([first.claim('first', true), second.claim('second', true)]);
      const leasedTwice = one.entries.some((e: { id: string }) =>
        two.entries.some((f: { id: string }) => f.id === e.id),
      );
      const owner = one.entries.length
        ? { tab: first, name: 'first', claim: one }
        : { tab: second, name: 'second', claim: two };
      await (owner.tab === first ? second : first).finish(owner.claim.entries, 'intruder', true);
      const afterWrongOwner = await first.stats();
      await owner.tab.finish(owner.claim.entries, owner.name, false);
      const backedOff = await second.claim('second', false);
      const forced = await second.claim('second', true);
      await second.finish(forced.entries, 'second', true);
      const payloadIsBytes = forced.entries.every((e: { payload: Uint8Array }) => e.payload instanceof Uint8Array);
      await first.put({ ...row('expired'), expires: Date.now() - 1 });
      const expired = await first.claim('first', true);
      await first.put(row('clear'));
      await first.clear();
      const result = {
        evicted,
        oversized,
        stats,
        leasedTwice,
        claimedTotal: one.entries.length + two.entries.length,
        afterWrongOwner,
        backedOff: backedOff.entries.length,
        forced: forced.entries.map((r: { id: string }) => r.id),
        payloadIsBytes,
        expired: expired.expired,
        empty: await first.stats(),
        neighbour: await neighbour.stats(),
        other: await other.stats(),
      };
      for (const o of [first, second, neighbour, other]) o.close();
      return result;
    },
    [
      `/@fs/${root}/packages/errors/src/indexeddb.ts`,
      `/@fs/${root}/packages/errors/src/kv-outbox.ts`,
      `/@fs/${root}/packages/errors/src/browser.ts`,
    ],
  );
  expect(result.evicted).toEqual({ stored: true, evicted: 1 });
  expect(result.oversized.stored).toBe(false);
  expect(result.stats).toMatchObject({ entries: 2, bytes: 8 });
  expect(result.leasedTwice).toBe(false);
  expect(result.claimedTotal).toBe(2);
  expect(result.afterWrongOwner).toMatchObject({ entries: 2, bytes: 8 });
  expect(result.backedOff).toBe(0);
  expect(result.forced).toEqual(['b', 'c']);
  expect(result.payloadIsBytes).toBe(true);
  expect(result.expired).toBe(1);
  expect(result.empty.entries).toBe(0);
  expect(result.neighbour.entries).toBe(1);
  expect(result.other.entries).toBe(0);
});

test('processor fixtures: retry, corrupt record, recursion, oversized bypass and storage failure', async ({ page }) => {
  await page.goto('http://127.0.0.1:4181');
  const result = await page.evaluate(async (modulePath) => {
    const { processorFixture } = await import(modulePath);
    return processorFixture();
  }, `/@fs/${root}/tests/errors-browser/outbox-fixture.ts`);
  expect(result.retryBefore.entries).toBe(1);
  expect(result.retryAfter.entries).toBe(0);
  expect(result.bodyStable).toBe(true);
  expect(result.diagnostics).toContain('outbox_export_failed');
  expect(result.diagnostics).toContain('outbox_capacity_bypass');
  expect(result.diagnostics).toContain('outbox_storage_failed');
  expect(result.diagnostics).toContain('outbox_invalid_record');
  expect(result.diagnostics).toContain('outbox_unknown_version');
  expect(result.dropped).toEqual({ invalid: 1, unknownVersion: 1 });
  expect(result.storageFlushFailed).toBe(true);
  expect(result.fallbackBodyLength).toBeGreaterThan(100_000);
  expect(result.fallbackCount).toBe(1);
  expect(result.reentrantEmits).toBe(0);
});

test('a dedicated worker runs the outbox and captures its own unhandled rejections', async ({ page }) => {
  await page.goto('http://127.0.0.1:4181');
  const result = await page.evaluate(
    (workerPath) =>
      new Promise((resolve) => {
        const worker = new Worker(workerPath, { type: 'module' });
        worker.onmessage = (event) => {
          resolve(event.data);
          worker.terminate();
        };
        worker.onerror = (event) => resolve({ failed: event.message });
      }),
    `/@fs/${root}/tests/errors-browser/outbox-worker.ts`,
  );
  // The fixture server has no /v1/logs: the export is rejected, the records stay in IndexedDB.
  expect(result).toEqual({ captured: 'emitted', emitted: 2, diagnostics: ['outbox_export_rejected'] });
});
