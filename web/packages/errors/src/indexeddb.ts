import type { KvEntry, KvKey, KvWrite, OrderedKv } from './kv.js';

const store = 'kv';
const openTimeoutMs = 3000;
const transactionTimeoutMs = 5000;

/** IndexedDB's key comparison is the KvKey order; an empty array sorts after every number and string. */
const prefixRange = (prefix: KvKey) => (prefix.length ? IDBKeyRange.bound([...prefix], [...prefix, []]) : undefined);

/**
 * An OrderedKv on IndexedDB, for a browser host that has no store of its
 * own: one database of name, one object store, array keys. A write batch is
 * one transaction. close ends it.
 */
export class IndexedDbKv<V> implements OrderedKv<V> {
  private database?: Promise<IDBDatabase>;
  private closed = false;

  constructor(private readonly name: string | Promise<string>) {}

  private open(): Promise<IDBDatabase> {
    if (this.closed) return Promise.reject(new Error('outbox_closed'));
    this.database ??= Promise.resolve(this.name).then(
      (name) =>
        new Promise<IDBDatabase>((resolve, reject) => {
          const request = indexedDB.open(name, 1);
          const timer = setTimeout(() => reject(new Error('outbox_open_timeout')), openTimeoutMs);
          const fail = () => {
            clearTimeout(timer);
            reject(new Error('outbox_unavailable'));
          };
          request.onerror = fail;
          request.onblocked = fail;
          request.onupgradeneeded = () => request.result.createObjectStore(store);
          request.onsuccess = () => {
            clearTimeout(timer);
            const database = request.result;
            if (this.closed) {
              database.close();
              reject(new Error('outbox_closed'));
              return;
            }
            database.onversionchange = () => this.close();
            resolve(database);
          };
        }),
    );
    return this.database;
  }

  private async run<T>(
    mode: IDBTransactionMode,
    work: (store: IDBObjectStore) => IDBRequest<T> | undefined,
  ): Promise<T | undefined> {
    const database = await this.open();
    return new Promise((resolve, reject) => {
      const tx = database.transaction(store, mode, { durability: 'strict' });
      const timer = setTimeout(() => {
        try {
          tx.abort();
        } catch {
          /* already finished */
        }
        reject(new Error('outbox_transaction_timeout'));
      }, transactionTimeoutMs);
      let request: IDBRequest<T> | undefined;
      tx.oncomplete = () => {
        clearTimeout(timer);
        resolve(request?.result);
      };
      tx.onabort = () => {
        clearTimeout(timer);
        reject(new Error('outbox_transaction_failed'));
      };
      request = work(tx.objectStore(store));
    });
  }

  get(key: KvKey): Promise<V | undefined> {
    return this.run<V>('readonly', (s) => s.get([...key]));
  }

  async scan(prefix: KvKey): Promise<KvEntry<V>[]> {
    const database = await this.open();
    return new Promise((resolve, reject) => {
      const tx = database.transaction(store, 'readonly');
      const entries: KvEntry<V>[] = [];
      const request = tx.objectStore(store).openCursor(prefixRange(prefix));
      request.onsuccess = () => {
        const cursor = request.result;
        if (!cursor) return;
        const key = cursor.key;
        if (Array.isArray(key) && key.every((part) => typeof part === 'string' || typeof part === 'number'))
          entries.push({ key, value: cursor.value });
        cursor.continue();
      };
      tx.oncomplete = () => resolve(entries);
      tx.onabort = () => reject(new Error('outbox_transaction_failed'));
    });
  }

  async write(batch: readonly KvWrite<V>[]): Promise<void> {
    await this.run<undefined>('readwrite', (s) => {
      for (const w of batch) {
        if ('put' in w) s.put(w.value, [...w.put]);
        else s.delete([...w.delete]);
      }
      return undefined;
    });
  }

  close() {
    this.closed = true;
    void this.database?.then(
      (database) => database.close(),
      () => {},
    );
  }
}
