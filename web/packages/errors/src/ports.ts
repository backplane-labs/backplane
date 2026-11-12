// The SDK core reaches its host only through these ports. A runtime (browser,
// worker, React Native, Node) supplies them; /browser ships the browser set.
import type { ExceptionData } from './protocol.js'

/** Time and identity of the host. */
export interface Runtime {
  /** Wall clock, Unix milliseconds. */
  now(): number
  /** Monotonic milliseconds, for durations and history age. */
  monotonic(): number
  /** A random version 4 UUID, lowercase; the wire schema requires that form. */
  randomId(): string
}

/** An error nobody caught, as the host reports it. */
export interface UncaughtError {
  readonly error: unknown
  readonly mechanism: Exclude<ExceptionData['mechanism'], 'manual'>
  readonly location?: ExceptionData['location']
}

/** The host's uncaught errors and unhandled rejections. */
export interface UncaughtSource {
  /** Starts reporting; the returned function stops it. */
  subscribe(report: (uncaught: UncaughtError) => void): () => void
}

/** Moments worth retrying delivery now: the network came back, the app is leaving. */
export interface DeliveryTriggers {
  /** urgent: the app may stop soon, so send everything pending. The returned function stops it. */
  subscribe(kick: (urgent: boolean) => void): () => void
}

/** Outbox sizes; storage enforces them on every put. */
export interface OutboxLimits {
  maxBytes: number
  maxEntries: number
  maxAgeMs: number
}

/** One export kept by an outbox. Private format, never part of the wire contract. */
export interface StoredExport {
  id: string
  payload: string
  bytes: number
  /** Unix ms. */
  created: number
  /** Unix ms. */
  expires: number
  /** Unix ms before which the entry waits, unless forced. */
  next: number
  attempts: number
  owner?: string
  /** Unix ms until which the owner holds the entry. */
  lease?: number
}

/**
 * Durable storage of an outbox, one per destination and account. Every method
 * is atomic against other processors sharing the storage (tabs, workers).
 */
export interface OutboxStorage {
  readonly limits: OutboxLimits
  /**
   * Stores entry; drops expired rows and evicts the oldest until the limits
   * hold. stored is false when entry alone exceeds maxBytes.
   */
  put(entry: StoredExport): Promise<{ stored: boolean; evicted: number }>
  /**
   * Leases up to 32 unexpired, unleased entries due now (any, when force) to
   * owner for 30 s, oldest first; deletes expired rows and counts them.
   */
  claim(owner: string, force: boolean): Promise<{ entries: StoredExport[]; expired: number }>
  /**
   * Ends owner's lease: deletes the entries on success, otherwise counts the
   * attempt and schedules the next one with retryDelay.
   */
  finish(entries: StoredExport[], owner: string, success: boolean): Promise<void>
  stats(): Promise<{ entries: number; bytes: number; oldestAgeMs: number }>
  clear(): Promise<void>
  close(): void
}

export interface OutboxOptions {
  /** Stable application/account namespace. Change on account or tenant changes. */
  name: string
  maxBytes?: number
  maxEntries?: number
  maxAgeMs?: number
}

/** Validated limits with defaults: 10 MiB, 1000 entries, 24 h. */
export function outboxLimits(options: OutboxOptions): OutboxLimits & { name: string } {
  const limits = {
    maxBytes: 10 * 1024 * 1024,
    maxEntries: 1000,
    maxAgeMs: 24 * 3600 * 1000,
    ...options,
  }
  if (
    !/^[a-zA-Z0-9_.-]{1,100}$/.test(limits.name) ||
    ![limits.maxBytes, limits.maxEntries, limits.maxAgeMs].every(
      (value) => Number.isSafeInteger(value) && value > 0
    )
  )
    throw new TypeError('invalid_outbox_options')
  return limits
}
