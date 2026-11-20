import { retryDelay } from './delivery.js';
import type { KvKey, KvWrite, Locks, OrderedKv } from './kv.js';
import type { OutboxLimits, OutboxStorage, StoredExport } from './ports.js';

/** Entries one claim leases, and for how long. */
const claimSize = 32;
const leaseMs = 30_000;

export interface KvOutboxOptions {
  /** The host's store; the outbox keeps its rows under prefix. */
  kv: OrderedKv<StoredExport>;
  /** Rows live at [...prefix, created, id], oldest first. */
  prefix: KvKey;
  limits: OutboxLimits;
  locks: Locks;
  /** Serializes every outbox of the same rows: one name per prefix. */
  lock: string;
  /** Wall clock, Unix ms. */
  now: () => number;
}

const valid = (row: StoredExport) =>
  row.payload instanceof Uint8Array &&
  Number.isSafeInteger(row.bytes) &&
  row.bytes >= 0 &&
  [row.created, row.expires, row.next, row.attempts].every(Number.isSafeInteger);

/**
 * OutboxStorage over an ordered key-value store: the lease logic (claim,
 * finish, expiry, eviction, retry) lives here once, each operation one
 * atomic batch under the lock.
 */
export function kvOutbox(options: KvOutboxOptions): OutboxStorage {
  const { kv, prefix, limits, locks, lock, now } = options;
  const key = (row: StoredExport): KvKey => [...prefix, row.created, row.id];
  let closed = false;
  const locked = <T>(work: () => Promise<T>) =>
    closed ? Promise.reject(new Error('outbox_closed')) : locks.run(lock, work);
  const rows = () => kv.scan(prefix);

  return {
    limits,
    put: (entry) =>
      locked(async () => {
        if (entry.bytes > limits.maxBytes) return { stored: false, evicted: 0 };
        const at = now();
        const batch: KvWrite<StoredExport>[] = [];
        let evicted = 0;
        const kept: StoredExport[] = [];
        for (const { key: stored, value: row } of await rows()) {
          if (!valid(row) || row.expires <= at) {
            batch.push({ delete: stored });
            evicted++;
          } else if (row.id !== entry.id) kept.push(row);
        }
        let bytes = entry.bytes + kept.reduce((sum, row) => sum + row.bytes, 0);
        let count = kept.length + 1;
        // Oldest first: kept is in key order.
        for (const row of kept) {
          if (bytes <= limits.maxBytes && count <= limits.maxEntries) break;
          batch.push({ delete: key(row) });
          bytes -= row.bytes;
          count--;
          evicted++;
        }
        batch.push({ put: key(entry), value: entry });
        await kv.write(batch);
        return { stored: true, evicted };
      }),
    claim: (owner, force) =>
      locked(async () => {
        const at = now();
        const batch: KvWrite<StoredExport>[] = [];
        const entries: StoredExport[] = [];
        let expired = 0;
        for (const { key: stored, value: row } of await rows()) {
          if (!valid(row) || row.expires <= at) {
            batch.push({ delete: stored });
            expired++;
          } else if (
            entries.length < claimSize &&
            (row.lease === undefined || row.lease <= at) &&
            (force || row.next <= at)
          ) {
            const leased = { ...row, owner, lease: at + leaseMs };
            batch.push({ put: key(leased), value: leased });
            entries.push(leased);
          }
        }
        if (batch.length) await kv.write(batch);
        return { entries, expired };
      }),
    finish: (entries, owner, success) =>
      locked(async () => {
        const at = now();
        const batch: KvWrite<StoredExport>[] = [];
        for (const entry of entries) {
          const row = await kv.get(key(entry));
          if (row === undefined || row.owner !== owner) continue;
          if (success) batch.push({ delete: key(row) });
          else {
            const { owner: _owner, lease: _lease, ...rest } = row;
            const attempts = row.attempts + 1;
            batch.push({ put: key(row), value: { ...rest, attempts, next: at + retryDelay(attempts) } });
          }
        }
        if (batch.length) await kv.write(batch);
      }),
    stats: () =>
      locked(async () => {
        const at = now();
        // Expired rows are deleted by claim and put, which count them.
        const live = (await rows()).map((entry) => entry.value).filter((row) => valid(row) && row.expires > at);
        return {
          entries: live.length,
          bytes: live.reduce((sum, row) => sum + row.bytes, 0),
          oldestAgeMs: live.reduce((oldest, row) => Math.max(oldest, at - row.created), 0),
        };
      }),
    clear: () =>
      locked(async () => {
        const batch = (await rows()).map((entry): KvWrite<StoredExport> => ({ delete: entry.key }));
        if (batch.length) await kv.write(batch);
      }),
    close: () => {
      closed = true;
    },
  };
}
