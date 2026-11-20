export type DeliveryOutcome = 'accepted' | 'retry' | 'rejected';

/** An HTTP answer: 2xx accepted, a throttle or gateway status retried, any other error rejected. */
export function statusOutcome(status: number): DeliveryOutcome {
  if (status >= 200 && status < 300) return 'accepted';
  if ([408, 429, 502, 503, 504].includes(status)) return 'retry';
  return 'rejected';
}

export function retryDelay(attempt: number, random = Math.random): number {
  const cap = Math.min(60_000, 1000 * 2 ** Math.min(attempt, 6));
  return Math.floor(cap / 2 + (random() * cap) / 2);
}

/** Posts OTLP protobuf request bodies to one receiver. */
export interface OtlpSender {
  /** urgent: the app may stop, so a small enough body outlives the page (fetch keepalive). */
  send(body: Uint8Array, urgent: boolean): Promise<DeliveryOutcome>;
}

/** fetch keepalive carries at most 64 KiB in flight per page. */
export const keepaliveBytes = 60_000;

export interface FetchSenderOptions {
  url: string;
  headers?: Record<string, string>;
  timeoutMs?: number;
  /** The host's fetch; the global one by default. */
  fetch?: typeof fetch;
}

/** An OtlpSender over fetch. A network failure or timeout is retried; the response body is never read. */
export function fetchSender(options: FetchSenderOptions): OtlpSender {
  const timeoutMs = options.timeoutMs ?? 10_000;
  return {
    async send(body, urgent) {
      const post = options.fetch ?? fetch;
      try {
        const response = await post(options.url, {
          method: 'POST',
          headers: { ...options.headers, 'Content-Type': 'application/x-protobuf' },
          // An ArrayBuffer-backed copy: fetch takes no view of a shared buffer.
          body: body.slice(),
          keepalive: urgent && body.byteLength <= keepaliveBytes,
          signal: AbortSignal.timeout(timeoutMs),
        });
        await response.body?.cancel();
        return statusOutcome(response.status);
      } catch {
        return 'retry';
      }
    },
  };
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
  unknownVersion: number;
  bypassed: number;
  unpersistedLost: number;
  storageFailures: number;
  lastAcceptedAt?: number;
}
