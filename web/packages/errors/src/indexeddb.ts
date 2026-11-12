import { retryDelay } from './delivery.js'
import type { OutboxLimits, OutboxStorage, StoredExport } from './ports.js'

/** OutboxStorage on IndexedDB: shared by the tabs and workers of one origin. */
export class IndexedDbOutbox implements OutboxStorage {
  private database?: IDBDatabase
  private opening?: Promise<IDBDatabase>
  private closed = false
  constructor(
    private readonly name: string | Promise<string>,
    readonly limits: OutboxLimits,
    private readonly now: () => number = Date.now
  ) {}
  private open() {
    if (this.closed) return Promise.reject(new Error('outbox_closed'))
    this.opening ??= Promise.resolve(this.name).then(
      (name) =>
        new Promise<IDBDatabase>((resolve, reject) => {
          const request = indexedDB.open(name, 1)
          let finished = false
          const timer = setTimeout(() => {
            finished = true
            reject(new Error('outbox_open_timeout'))
          }, 3000)
          const fail = () => {
            finished = true
            clearTimeout(timer)
            reject(new Error('outbox_unavailable'))
          }
          request.onerror = fail
          request.onblocked = fail
          request.onupgradeneeded = () => {
            const store = request.result.createObjectStore('records', { keyPath: 'id' })
            store.createIndex('created', 'created')
          }
          request.onsuccess = () => {
            clearTimeout(timer)
            if (finished || this.closed) {
              request.result.close()
              reject(new Error('outbox_closed'))
              return
            }
            this.database = request.result
            this.database.onversionchange = () => this.close()
            resolve(this.database)
          }
        })
    )
    return this.opening
  }
  async transaction<T>(
    work: (store: IDBObjectStore, done: (result: T) => void) => void
  ): Promise<T> {
    const database = await this.open()
    return new Promise<T>((resolve, reject) => {
      const tx = database.transaction('records', 'readwrite', { durability: 'strict' })
      let result: T
      const timer = setTimeout(() => {
        try {
          tx.abort()
        } catch {}
        reject(new Error('outbox_transaction_timeout'))
      }, 5000)
      tx.oncomplete = () => {
        clearTimeout(timer)
        resolve(result)
      }
      tx.onabort = () => {
        clearTimeout(timer)
        reject(new Error('outbox_transaction_failed'))
      }
      tx.onerror = () => {
        /* onabort reports the transaction failure */
      }
      try {
        work(tx.objectStore('records'), (value) => {
          result = value
        })
      } catch (error) {
        tx.abort()
        reject(error)
      }
    })
  }
  async put(entry: StoredExport) {
    return this.transaction<{ stored: boolean; evicted: number }>((store, done) => {
      let bytes = entry.bytes,
        count = 1,
        evicted = 0
      const now = this.now()
      const existing: StoredExport[] = []
      const request = store.index('created').openCursor()
      request.onsuccess = () => {
        const cursor = request.result
        if (cursor) {
          const row = cursor.value as StoredExport
          if (row.expires <= now || !Number.isSafeInteger(row.bytes) || row.bytes < 0) {
            cursor.delete()
            evicted++
          } else if (row.id !== entry.id) {
            bytes += row.bytes
            count++
            existing.push(row)
          }
          cursor.continue()
          return
        }
        if (entry.bytes > this.limits.maxBytes) {
          done({ stored: false, evicted })
          return
        }
        for (const row of existing) {
          if (bytes <= this.limits.maxBytes && count <= this.limits.maxEntries) break
          store.delete(row.id)
          bytes -= row.bytes
          count--
          evicted++
        }
        store.put(entry)
        done({ stored: true, evicted })
      }
    })
  }
  async claim(owner: string, force: boolean) {
    return this.transaction<{ entries: StoredExport[]; expired: number }>((store, done) => {
      const now = this.now(),
        entries: StoredExport[] = []
      let expired = 0
      const request = store.index('created').openCursor()
      request.onsuccess = () => {
        const cursor = request.result
        if (!cursor) {
          done({ entries, expired })
          return
        }
        const row = cursor.value as StoredExport
        if (row.expires <= now) {
          cursor.delete()
          expired++
        } else if (
          entries.length < 32 &&
          (!row.lease || row.lease <= now) &&
          (force || row.next <= now)
        ) {
          row.owner = owner
          row.lease = now + 30_000
          cursor.update(row)
          entries.push(row)
        }
        cursor.continue()
      }
    })
  }
  async finish(entries: StoredExport[], owner: string, success: boolean) {
    return this.transaction<void>((store, done) => {
      const now = this.now()
      for (const entry of entries) {
        const request = store.get(entry.id)
        request.onsuccess = () => {
          const row = request.result as StoredExport | undefined
          if (!row || row.owner !== owner) return
          if (success) store.delete(row.id)
          else {
            row.attempts++
            row.next = now + retryDelay(row.attempts)
            delete row.owner
            delete row.lease
            store.put(row)
          }
        }
      }
      done(undefined)
    })
  }
  async stats() {
    return this.transaction<{ entries: number; bytes: number; oldestAgeMs: number }>(
      (store, done) => {
        const now = this.now()
        let entries = 0,
          bytes = 0,
          oldestAgeMs = 0
        const request = store.openCursor()
        request.onsuccess = () => {
          const cursor = request.result
          if (!cursor) {
            done({ entries, bytes, oldestAgeMs })
            return
          }
          const row = cursor.value as StoredExport
          // Expiry is deleted by claim/put, which also report the diagnostic count.
          if (row.expires > now) {
            entries++
            bytes += row.bytes
            oldestAgeMs = Math.max(oldestAgeMs, now - row.created)
          }
          cursor.continue()
        }
      }
    )
  }
  async clear() {
    return this.transaction<void>((store, done) => {
      store.clear()
      done(undefined)
    })
  }
  close() {
    this.closed = true
    this.database?.close()
  }
}
