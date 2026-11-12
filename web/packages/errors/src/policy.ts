import { materialize } from './json.js'
import type { ExceptionData, JsonValue } from './protocol.js'

export type DataArea =
  | 'source'
  | 'inline'
  | 'breadcrumb'
  | 'exception'
  | 'attributes'
  | 'extensions'
  | 'label'
export interface SanitizeContext {
  readonly area: DataArea
  readonly name?: string
}
/** Runs before retaining data. Return JSON; throw to omit the section, never to send raw data. */
export type Sanitizer = (value: JsonValue, context: SanitizeContext) => JsonValue
export interface RateLimit {
  burst: number
  perSecond: number
}
export interface ClientStats {
  /** Successful local reads; unavailable calls do not increment this counter. */
  snapshots: number
  attempted: number
  emitted: number
  filtered: number
  rateLimited: number
  failed: number
  reentrant: number
  historyEntries: number
  historyEvicted: number
  lastCaptureMs: number
  maxCaptureMs: number
  diagnostics: Readonly<Record<string, number>>
}

/** Explicit key-based policy. Strings such as stacktraces need an application-specific sanitizer. */
export function redactKeys(keys: readonly string[], replacement = '[REDACTED]'): Sanitizer {
  const sensitive = new Set(keys.map((key) => key.toLowerCase()))
  return (input) => {
    const walk = (value: JsonValue): JsonValue => {
      if (!value || typeof value !== 'object') return value
      if (Array.isArray(value)) return value.map(walk)
      return Object.fromEntries(
        Object.entries(value).map(([key, child]) => [
          key,
          sensitive.has(key.toLowerCase()) ? replacement : walk(child),
        ])
      )
    }
    return walk(materialize(input))
  }
}

export function tokenBucket(limits: RateLimit | undefined, monotonic: () => number) {
  if (
    limits &&
    (!Number.isFinite(limits.perSecond) ||
      limits.perSecond <= 0 ||
      !Number.isSafeInteger(limits.burst) ||
      limits.burst < 1)
  )
    throw new TypeError('invalid_rate_limit')
  let tokens = limits?.burst ?? 0,
    last = monotonic()
  return () => {
    if (!limits) return true
    const now = monotonic()
    tokens = Math.min(limits.burst, tokens + (Math.max(0, now - last) * limits.perSecond) / 1000)
    last = now
    if (tokens < 1) return false
    tokens--
    return true
  }
}

export type CaptureFilter = (exception: Readonly<ExceptionData>) => boolean
