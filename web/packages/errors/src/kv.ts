// The host's ordered key-value store and its locks: the two ports an outbox
// needs. The SDK keeps the outbox logic itself (kv-outbox.ts) and never opens
// a database of its own.

/** A key: its parts compare in order, numbers before strings, numbers numerically, strings by code unit. */
export type KvKey = readonly (string | number)[];

export interface KvEntry<V> {
  readonly key: KvKey;
  readonly value: V;
}

/** One write of a batch: a put or a delete. */
export type KvWrite<V> = { readonly put: KvKey; readonly value: V } | { readonly delete: KvKey };

/**
 * An ordered key-value store. Values are structured-clonable objects whose
 * Uint8Array fields stay bytes. write applies its batch atomically: all of it
 * or none.
 */
export interface OrderedKv<V> {
  get(key: KvKey): Promise<V | undefined>;
  /** Every entry whose key starts with prefix, in key order. */
  scan(prefix: KvKey): Promise<KvEntry<V>[]>;
  write(batch: readonly KvWrite<V>[]): Promise<void>;
}

/**
 * Exclusive locks by name. run holds name while work runs, across every
 * processor sharing the store (tabs and workers of an origin with Web Locks;
 * one process with memoryLocks).
 */
export interface Locks {
  run<T>(name: string, work: () => Promise<T>): Promise<T>;
}

function comparePart(a: string | number, b: string | number): number {
  if (typeof a !== typeof b) return typeof a === 'number' ? -1 : 1;
  return a < b ? -1 : a > b ? 1 : 0;
}

/** The order of KvKey, the same as IndexedDB's for arrays of numbers and strings. */
export function compareKeys(a: KvKey, b: KvKey): number {
  for (let i = 0; i < Math.min(a.length, b.length); i++) {
    const order = comparePart(a[i], b[i]);
    if (order) return order;
  }
  return a.length - b.length;
}

const startsWith = (key: KvKey, prefix: KvKey) =>
  key.length >= prefix.length && prefix.every((part, i) => part === key[i]);

/** An OrderedKv in memory: one process (Node, Electron main, tests). */
export function memoryKv<V>(): OrderedKv<V> {
  const rows = new Map<string, KvEntry<V>>();
  const id = (key: KvKey) => JSON.stringify(key);
  return {
    get: (key) => Promise.resolve(rows.get(id(key))?.value),
    scan: (prefix) =>
      Promise.resolve(
        [...rows.values()].filter((e) => startsWith(e.key, prefix)).sort((a, b) => compareKeys(a.key, b.key)),
      ),
    write: (batch) => {
      for (const w of batch) {
        if ('put' in w) rows.set(id(w.put), { key: [...w.put], value: w.value });
        else rows.delete(id(w.delete));
      }
      return Promise.resolve();
    },
  };
}

/** Locks of one process (Node, Electron main): a queue per name. */
export function memoryLocks(): Locks {
  const tails = new Map<string, Promise<void>>();
  return {
    run<T>(name: string, work: () => Promise<T>): Promise<T> {
      const previous = tails.get(name) ?? Promise.resolve();
      const result = previous.then(work, work);
      const tail = result.then(
        () => {},
        () => {},
      );
      tails.set(name, tail);
      void tail.then(() => {
        if (tails.get(name) === tail) tails.delete(name);
      });
      return result;
    },
  };
}
