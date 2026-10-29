import { deliveryOutcome, type DeliveryOutcome, type DeliveryStats } from './delivery.js'
import type {
  LogRecordExporter,
  LogRecordProcessor,
  ReadableLogRecord,
} from '@opentelemetry/sdk-logs'
import { decodeRecord, encodeRecord } from './outbox-record.js'
import { OutboxStore, outboxLimits, type OutboxOptions, type StoredExport } from './outbox-store.js'
import { globalScope } from './scope.js'

export type { OutboxOptions } from './outbox-store.js'

/** Opt-in processor for the owned browser provider, using the standard protobuf exporter. */
export class PersistentLogProcessor implements LogRecordProcessor {
  private readonly counters: DeliveryStats = {
    exportAttempts: 0,
    accepted: 0,
    retried: 0,
    rejected: 0,
    persisted: 0,
    evicted: 0,
    expired: 0,
    invalid: 0,
    bypassed: 0,
    unpersistedLost: 0,
    storageFailures: 0,
  }
  private readonly owner = crypto.randomUUID()
  private readonly limits
  private readonly store: Promise<OutboxStore>
  private readonly writes = new Set<Promise<void>>()
  private pendingBytes = 0
  private inFlight = 0
  private running?: Promise<void>
  private stopped = false
  private storageFailed = false
  private diagnosing = false
  private timer: ReturnType<typeof setInterval>
  private shutdownPromise?: Promise<void>
  private readonly online = () => this.kick()
  private readonly visibility = () => {
    if (document.visibilityState === 'hidden') this.kick(true)
  }

  constructor(
    private readonly exporter: LogRecordExporter,
    url: string,
    options: OutboxOptions,
    private readonly onDiagnostic: (code: string) => void
  ) {
    this.limits = outboxLimits(options)
    // Bind replay to the destination without storing the URL, headers, or credentials.
    this.store = crypto.subtle
      .digest('SHA-256', new TextEncoder().encode(new URL(url, location.href).href))
      .then(
        (bytes) =>
          new OutboxStore(
            `backplane.errors.outbox.v1.${this.limits.name}.${Array.from(new Uint8Array(bytes), (n) => n.toString(16).padStart(2, '0')).join('')}`,
            this.limits
          )
      )
    void this.store.catch(() => this.report('outbox_unavailable'))
    // A worker has no document: its global scope still sends 'online', and the timer replays the rest.
    globalScope()?.addEventListener('online', this.online)
    if (typeof document !== 'undefined') document.addEventListener('visibilitychange', this.visibility)
    this.timer = setInterval(() => this.kick(), 1000)
    this.kick()
  }

  private report(code: string) {
    if (this.diagnosing) return
    this.diagnosing = true
    try {
      this.onDiagnostic(code)
    } catch {
      /* Diagnostic code cannot cause another capture. */
    } finally {
      this.diagnosing = false
    }
  }

  onEmit(record: ReadableLogRecord): void {
    if (this.stopped || this.diagnosing) return
    try {
      const payload = encodeRecord(record)
      const bytes = new TextEncoder().encode(payload).byteLength
      if (
        bytes > this.limits.maxBytes ||
        this.pendingBytes + bytes > this.limits.maxBytes ||
        this.writes.size >= this.limits.maxEntries
      ) {
        this.counters.bypassed++
        this.report('outbox_capacity_bypass')
        this.fallback(record)
        return
      }
      const created = Date.now()
      const entry: StoredExport = {
        id: crypto.randomUUID(),
        payload,
        bytes,
        created,
        expires: created + this.limits.maxAgeMs,
        next: 0,
        attempts: 0,
      }
      this.pendingBytes += bytes
      const write = this.persist(entry, record).finally(() => {
        this.pendingBytes -= bytes
        this.writes.delete(write)
      })
      this.writes.add(write)
    } catch {
      this.report('outbox_encode_failed')
      this.fallback(record)
    }
  }

  private async persist(entry: StoredExport, record: ReadableLogRecord) {
    try {
      const result = await (await this.store).put(entry)
      if (result.evicted) {
        this.counters.evicted += result.evicted
        this.report('outbox_evicted')
      }
      if (!result.stored) {
        this.counters.bypassed++
        this.report('outbox_capacity_bypass')
        this.fallback(record)
      } else {
        this.counters.persisted++
        this.kick()
      }
    } catch {
      this.counters.storageFailures++
      this.storageFailed = true
      this.report('outbox_storage_failed')
      this.fallback(record)
    }
  }

  private fallback(record: ReadableLogRecord) {
    void this.send([record]).then((success) => {
      if (success !== 'accepted') {
        this.counters.unpersistedLost++
        this.report('outbox_unpersisted_export_failed')
      }
    })
  }

  private send(records: ReadableLogRecord[]): Promise<DeliveryOutcome> {
    if (this.inFlight >= 4) {
      this.report('outbox_export_busy')
      return Promise.resolve('retry')
    }
    this.inFlight++
    this.counters.exportAttempts++
    return new Promise((resolve) => {
      let finished = false
      const done = (outcome: DeliveryOutcome) => {
        if (finished) return
        finished = true
        clearTimeout(timer)
        this.inFlight--
        if (outcome === 'accepted') {
          this.counters.accepted += records.length
          this.counters.lastAcceptedAt = Date.now()
        } else if (outcome === 'rejected') this.counters.rejected += records.length
        else this.counters.retried += records.length
        resolve(outcome)
      }
      // Owned exporter has a shorter timeout; the extra guard also isolates exporter bugs.
      const timer = setTimeout(() => done('retry'), 12_000)
      try {
        this.exporter.export(records, (result) => done(deliveryOutcome(result)))
      } catch {
        done('retry')
      }
    })
  }

  private kick(force = false) {
    if (!this.stopped) void this.drain(force)
  }

  private drain(force: boolean): Promise<void> {
    this.running ??= this.run(force)
      .catch(() => this.report('outbox_retry_failed'))
      .finally(() => {
        this.running = undefined
      })
    return this.running
  }

  private async run(force: boolean) {
    const store = await this.store
    let remaining = force ? this.limits.maxEntries : 32
    while (remaining > 0) {
      const claimed = await store.claim(this.owner, force)
      if (claimed.expired) {
        this.counters.expired += claimed.expired
        this.report('outbox_expired')
      }
      if (!claimed.entries.length) return
      const entries: StoredExport[] = [],
        records: ReadableLogRecord[] = []
      for (const entry of claimed.entries) {
        try {
          records.push(decodeRecord(entry.payload))
          entries.push(entry)
        } catch {
          await store.finish([entry], this.owner, true)
          this.counters.invalid++
          this.report('outbox_invalid_record')
        }
      }
      remaining -= claimed.entries.length
      if (!entries.length) continue
      const success = await this.send(records)
      await store.finish(entries, this.owner, success !== 'retry')
      if (success === 'rejected') this.report('outbox_export_rejected')
      if (success === 'retry') {
        this.report('outbox_export_failed')
        return
      }
    }
  }

  /** Waits for IndexedDB transactions, not OTLP delivery. Rejects after storage failure. */
  async flushStorage(): Promise<void> {
    await Promise.all([...this.writes])
    if (this.storageFailed) throw new Error('outbox_storage_failed')
  }

  async stats() {
    await this.flushStorage()
    return { ...(await (await this.store).stats()), ...this.counters }
  }

  async clear() {
    await Promise.all([...this.writes])
    await (await this.store).clear()
  }

  async forceFlush(): Promise<void> {
    await this.flushStorage()
    if (this.running) await this.running
    await this.drain(true)
    await this.exporter.forceFlush()
  }

  stop() {
    this.stopped = true
    clearInterval(this.timer)
    globalScope()?.removeEventListener('online', this.online)
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', this.visibility)
  }

  shutdown(): Promise<void> {
    this.stop()
    this.shutdownPromise ??= (async () => {
      try {
        await this.forceFlush()
      } finally {
        try {
          await this.exporter.shutdown()
        } finally {
          ;(await this.store).close()
        }
      }
    })()
    return this.shutdownPromise
  }
}
