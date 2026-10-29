import { resolve } from 'node:path'
import { expect, test } from '@playwright/test'

const root = resolve(import.meta.dirname, '../..')

test('native IndexedDB storage: budgets, expiry, leases, clear and namespace isolation', async ({
  page,
}) => {
  await page.goto('http://127.0.0.1:4181')
  const result = await page.evaluate(async (modulePath) => {
    const { OutboxStore, outboxLimits } = await import(modulePath)
    const name = `storage-${crypto.randomUUID()}`
    const limits = outboxLimits({ name, maxBytes: 10, maxEntries: 2 })
    const first = new OutboxStore(name, limits),
      second = new OutboxStore(name, limits)
    const other = new OutboxStore(`${name}-other`, limits)
    const now = Date.now()
    const row = (id: string, bytes = 4) => ({
      id,
      bytes,
      payload: id,
      created: now,
      expires: now + 3600_000,
      next: 0,
      attempts: 0,
    })
    try {
      await first.put(row('a'))
      await first.put(row('b'))
      const evicted = await first.put(row('c'))
      const oversized = await first.put(row('oversized', 11))
      const stats = await first.stats()
      const owner = await first.claim('first', false)
      const competitor = await second.claim('second', true)
      await second.finish(owner.entries, 'second', true) // a different owner cannot acknowledge
      const afterWrongOwner = await first.stats()
      await first.finish(owner.entries, 'first', false)
      const backedOff = await second.claim('second', false)
      const forced = await second.claim('second', true)
      await second.finish(forced.entries, 'second', true)
      await first.put({ ...row('expired'), expires: now - 1 })
      const expired = await first.claim('first', true)
      await first.put(row('clear'))
      await first.clear()
      return {
        evicted,
        oversized,
        stats,
        competitor: competitor.entries.length,
        afterWrongOwner,
        backedOff: backedOff.entries.length,
        forced: forced.entries.map((r: { id: string }) => r.id),
        expired: expired.expired,
        empty: await first.stats(),
        other: await other.stats(),
      }
    } finally {
      first.close()
      second.close()
      other.close()
    }
  }, `/@fs/${root}/packages/errors/src/outbox-store.ts`)
  expect(result.evicted).toEqual({ stored: true, evicted: 1 })
  expect(result.oversized.stored).toBe(false)
  expect(result.stats).toMatchObject({ entries: 2, bytes: 8 })
  expect(result.stats.oldestAgeMs).toBeGreaterThanOrEqual(0)
  expect(result.afterWrongOwner).toMatchObject({ entries: 2, bytes: 8 })
  expect(result.competitor).toBe(0)
  expect(result.backedOff).toBe(0)
  expect(result.forced).toEqual(['b', 'c'])
  expect(result.expired).toBe(1)
  expect(result.empty.entries).toBe(0)
  expect(result.other.entries).toBe(0)
})

test('processor fixtures: retry, corrupt record, recursion, oversized bypass and storage failure', async ({
  page,
}) => {
  await page.goto('http://127.0.0.1:4181')
  const result = await page.evaluate(async (modulePath) => {
    const { processorFixture } = await import(modulePath)
    return processorFixture()
  }, `/@fs/${root}/tests/errors-browser/outbox-fixture.ts`)
  expect(result.retryBefore.entries).toBe(1)
  expect(result.retryAfter.entries).toBe(0)
  expect(result.bodyStable).toBe(true)
  expect(result.diagnostics).toContain('outbox_export_failed')
  expect(result.diagnostics).toContain('outbox_capacity_bypass')
  expect(result.diagnostics).toContain('outbox_storage_failed')
  expect(result.diagnostics).toContain('outbox_invalid_record')
  expect(result.storageFlushFailed).toBe(true)
  expect(result.fallbackBodyLength).toBeGreaterThan(100_000)
  expect(result.fallbackCount).toBe(1)
  expect(result.reentrantEmits).toBe(0)
})

test('a dedicated worker runs the outbox and captures its own unhandled rejections', async ({ page }) => {
  await page.goto('http://127.0.0.1:4181')
  const result = await page.evaluate(
    (workerPath) =>
      new Promise((resolve) => {
        const worker = new Worker(workerPath, { type: 'module' })
        worker.onmessage = (event) => {
          resolve(event.data)
          worker.terminate()
        }
        worker.onerror = (event) => resolve({ failed: event.message })
      }),
    `/@fs/${root}/tests/errors-browser/outbox-worker.ts`
  )
  // The fixture server has no /v1/logs: the export is rejected, the records stay in IndexedDB.
  expect(result).toEqual({ captured: 'emitted', emitted: 2, diagnostics: ['outbox_export_rejected'] })
})
