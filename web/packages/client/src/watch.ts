import {
  CODE_DEADLINE_EXCEEDED,
  CODE_RESOURCE_EXHAUSTED,
  CODE_UNAVAILABLE,
  WsStatusError,
} from '@gopherex/ws-proto-transport';

export interface WatchRetryOptions {
  signal: AbortSignal;
  /** Called on interrupted attempts, including unexpected clean EOF. */
  onRetry?: (error: WsStatusError) => void;
  /** Test/deployment override; production defaults to capped jittered backoff. */
  retryDelay?: (attempt: number) => number;
}

/** Only long-lived read watches. Never wrap a mutation or a finite stream. */
export async function* watchWithRetry<T>(
  open: (signal: AbortSignal) => AsyncIterable<T>,
  options: WatchRetryOptions,
): AsyncIterable<T> {
  let attempt = 0;
  while (!options.signal.aborted) {
    try {
      for await (const value of open(options.signal)) {
        if (options.signal.aborted) return;
        attempt = 0;
        yield value;
      }
      if (options.signal.aborted) return;
      throw new WsStatusError(CODE_UNAVAILABLE, 'Watch ended before cancellation');
    } catch (error) {
      if (options.signal.aborted) return;
      if (
        !(error instanceof WsStatusError) ||
        ![CODE_UNAVAILABLE, CODE_DEADLINE_EXCEEDED, CODE_RESOURCE_EXHAUSTED].includes(error.code)
      )
        throw error;
      options.onRetry?.(error);
      const delay =
        options.retryDelay?.(attempt) ?? 250 + Math.random() * Math.min(30_000, 500 * 2 ** Math.min(attempt, 8));
      attempt++;
      await pause(Math.max(0, Math.min(30_000, delay)), options.signal);
    }
  }
}

function pause(milliseconds: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const finish = () => {
      clearTimeout(timer);
      signal.removeEventListener('abort', finish);
      resolve();
    };
    const timer = setTimeout(finish, milliseconds);
    signal.addEventListener('abort', finish, { once: true });
  });
}
