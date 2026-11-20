import type { LogRecordProcessor, ReadableLogRecord } from '@opentelemetry/sdk-logs';
import { type DeliveryOutcome, type DeliveryStats, keepaliveBytes, type OtlpSender } from './delivery.js';
import { batchRequests, encodeRecord, openRecord } from './outbox-record.js';
import type { DeliveryTriggers, OutboxLimits, OutboxStorage, Runtime, StoredExport } from './ports.js';

/** Where an outbox keeps exports and what tells it to retry. */
export interface OutboxPorts {
  storage: OutboxStorage;
  /** Without triggers the outbox retries on its 1 s timer only. */
  triggers?: DeliveryTriggers;
  /**
   * The largest request body sent, below the receiver's request limit
   * (Backplane's BACKPLANE_OTLP_BODY_BYTES is 2 MiB). Default 1 MiB.
   */
  maxBatchBytes?: number;
}

const defaultBatchBytes = 1024 * 1024;
const maxInFlight = 4;

/** Persists every export before sending it, and replays what is pending. */
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
    unknownVersion: 0,
    bypassed: 0,
    unpersistedLost: 0,
    storageFailures: 0,
  };
  private readonly owner: string;
  private readonly limits: OutboxLimits;
  private readonly store: OutboxStorage;
  private readonly maxBatchBytes: number;
  private readonly writes = new Set<Promise<void>>();
  private pendingBytes = 0;
  private inFlight = 0;
  private running?: Promise<void>;
  private stopped = false;
  private storageFailed = false;
  private diagnosing = false;
  private timer: ReturnType<typeof setInterval>;
  private shutdownPromise?: Promise<void>;
  private readonly untrigger: () => void;

  constructor(
    private readonly sender: OtlpSender,
    ports: OutboxPorts,
    private readonly runtime: Runtime,
    private readonly onDiagnostic: (code: string) => void,
  ) {
    const maxBatchBytes = ports.maxBatchBytes ?? defaultBatchBytes;
    if (!Number.isSafeInteger(maxBatchBytes) || maxBatchBytes <= 0) throw new TypeError('invalid_outbox_options');
    this.maxBatchBytes = maxBatchBytes;
    this.store = ports.storage;
    this.limits = ports.storage.limits;
    this.owner = runtime.randomId();
    this.untrigger = ports.triggers?.subscribe((urgent) => this.kick(urgent)) ?? (() => {});
    this.timer = setInterval(() => this.kick(), 1000);
    this.kick();
  }

  private report(code: string) {
    if (this.diagnosing) return;
    this.diagnosing = true;
    try {
      this.onDiagnostic(code);
    } catch {
      /* Diagnostic code cannot cause another capture. */
    } finally {
      this.diagnosing = false;
    }
  }

  onEmit(record: ReadableLogRecord): void {
    if (this.stopped || this.diagnosing) return;
    let payload: Uint8Array;
    try {
      payload = encodeRecord(record);
    } catch {
      this.report('outbox_encode_failed');
      return;
    }
    const bytes = payload.byteLength;
    if (
      bytes > this.limits.maxBytes ||
      this.pendingBytes + bytes > this.limits.maxBytes ||
      this.writes.size >= this.limits.maxEntries
    ) {
      this.counters.bypassed++;
      this.report('outbox_capacity_bypass');
      this.fallback(payload);
      return;
    }
    const created = this.runtime.now();
    const entry: StoredExport = {
      id: this.runtime.randomId(),
      payload,
      bytes,
      created,
      expires: created + this.limits.maxAgeMs,
      next: 0,
      attempts: 0,
    };
    this.pendingBytes += bytes;
    const write = this.persist(entry).finally(() => {
      this.pendingBytes -= bytes;
      this.writes.delete(write);
    });
    this.writes.add(write);
  }

  private async persist(entry: StoredExport) {
    try {
      const result = await this.store.put(entry);
      if (result.evicted) {
        this.counters.evicted += result.evicted;
        this.report('outbox_evicted');
      }
      if (!result.stored) {
        this.counters.bypassed++;
        this.report('outbox_capacity_bypass');
        this.fallback(entry.payload);
      } else {
        this.counters.persisted++;
        this.kick();
      }
    } catch {
      this.counters.storageFailures++;
      this.storageFailed = true;
      this.report('outbox_storage_failed');
      this.fallback(entry.payload);
    }
  }

  /** Sends a record the outbox could not keep, once. */
  private fallback(payload: Uint8Array) {
    const opened = openRecord(payload);
    if (opened.kind !== 'otlp') return;
    void this.send(opened.request, 1, false).then((outcome) => {
      if (outcome !== 'accepted') {
        this.counters.unpersistedLost++;
        this.report('outbox_unpersisted_export_failed');
      }
    });
  }

  private async send(body: Uint8Array, records: number, urgent: boolean): Promise<DeliveryOutcome> {
    if (this.inFlight >= maxInFlight) {
      this.report('outbox_export_busy');
      return 'retry';
    }
    this.inFlight++;
    this.counters.exportAttempts++;
    let outcome: DeliveryOutcome;
    try {
      outcome = await this.sender.send(body, urgent);
    } catch {
      outcome = 'retry';
    } finally {
      this.inFlight--;
    }
    if (outcome === 'accepted') {
      this.counters.accepted += records;
      this.counters.lastAcceptedAt = this.runtime.now();
    } else if (outcome === 'rejected') this.counters.rejected += records;
    else this.counters.retried += records;
    return outcome;
  }

  private kick(urgent = false) {
    if (!this.stopped) void this.drain(urgent);
  }

  private drain(urgent: boolean): Promise<void> {
    this.running ??= this.run(urgent)
      .catch(() => this.report('outbox_retry_failed'))
      .finally(() => {
        this.running = undefined;
      });
    return this.running;
  }

  /** Sends what is due (everything, when urgent) in bodies bounded by bytes. */
  private async run(urgent: boolean) {
    const store = this.store;
    // A page that is leaving sends keepalive bodies, which fetch bounds.
    const maxBody = urgent ? Math.min(this.maxBatchBytes, keepaliveBytes) : this.maxBatchBytes;
    let remaining = urgent ? this.limits.maxEntries : 32;
    while (remaining > 0) {
      const claimed = await store.claim(this.owner, urgent);
      if (claimed.expired) {
        this.counters.expired += claimed.expired;
        this.report('outbox_expired');
      }
      if (!claimed.entries.length) return;
      remaining -= claimed.entries.length;
      const entries: StoredExport[] = [];
      const requests: Uint8Array[] = [];
      const dropped: StoredExport[] = [];
      for (const entry of claimed.entries) {
        const opened = openRecord(entry.payload);
        switch (opened.kind) {
          case 'otlp':
            entries.push(entry);
            requests.push(opened.request);
            break;
          case 'unknown_version':
            dropped.push(entry);
            this.counters.unknownVersion++;
            this.report('outbox_unknown_version');
            break;
          case 'invalid':
            dropped.push(entry);
            this.counters.invalid++;
            this.report('outbox_invalid_record');
            break;
        }
      }
      if (dropped.length) await store.finish(dropped, this.owner, true);
      let sent = 0;
      for (const { body, count } of batchRequests(requests, maxBody)) {
        const outcome = await this.send(body, count, urgent);
        if (outcome === 'retry') {
          // This body and the rest of the claim are retried later, with backoff.
          await store.finish(entries.slice(sent), this.owner, false);
          this.report('outbox_export_failed');
          return;
        }
        await store.finish(entries.slice(sent, sent + count), this.owner, true);
        sent += count;
        if (outcome === 'rejected') this.report('outbox_export_rejected');
      }
    }
  }

  /** Waits for storage writes, not OTLP delivery. Rejects after storage failure. */
  async flushStorage(): Promise<void> {
    await Promise.all([...this.writes]);
    if (this.storageFailed) throw new Error('outbox_storage_failed');
  }

  async stats() {
    await this.flushStorage();
    return { ...(await this.store.stats()), ...this.counters };
  }

  async clear() {
    await Promise.all([...this.writes]);
    await this.store.clear();
  }

  async forceFlush(): Promise<void> {
    await this.flushStorage();
    if (this.running !== undefined) await this.running;
    await this.drain(true);
  }

  stop() {
    this.stopped = true;
    clearInterval(this.timer);
    this.untrigger();
  }

  shutdown(): Promise<void> {
    this.stop();
    this.shutdownPromise ??= (async () => {
      try {
        await this.forceFlush();
      } finally {
        this.store.close();
      }
    })();
    return this.shutdownPromise;
  }
}
