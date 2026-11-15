export type DeliveryOutcome = 'accepted' | 'retry' | 'rejected';
/** Pinned OTel 0.222.0: FetchTransport exposes permanent HTTP status in this exact error. */
export function deliveryOutcome(result: { code: number; error?: Error }): DeliveryOutcome {
  if (result.code === 0) return 'accepted';
  const error = result.error;
  // `code` is not part of Error's type: read it dynamically, then narrow.
  const code = error === undefined ? undefined : Reflect.get(error, 'code');
  const status =
    typeof code === 'number' && code >= 400
      ? code
      : Number(/^Fetch request failed with non-retryable status (\d{3})$/.exec(error?.message ?? '')?.[1]);
  if (status >= 400 && status <= 599 && ![429, 502, 503, 504].includes(status)) return 'rejected';
  // Unknown exporter/network failures retain data. Never inspect or log response bodies.
  return 'retry';
}

export function retryDelay(attempt: number, random = Math.random): number {
  const cap = Math.min(60_000, 1000 * 2 ** Math.min(attempt, 6));
  return Math.floor(cap / 2 + (random() * cap) / 2);
}

export interface DeliveryStats {
  exportAttempts: number;
  accepted: number;
  retried: number;
  rejected: number;
  persisted: number;
  evicted: number;
  expired: number;
  invalid: number;
  bypassed: number;
  unpersistedLost: number;
  storageFailures: number;
  lastAcceptedAt?: number;
}
