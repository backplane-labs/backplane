import { WsStatusError } from '@gopherex/ws-proto-transport';
import { afterEach, expect, test, vi } from 'vitest';
import { watchWithRetry } from '../packages/client/src/watch';

afterEach(() => vi.useRealTimers());

test.each([4, 8, 14])('watch recovers from code %i and releases the old iterator', async (code) => {
  vi.useFakeTimers();
  const controller = new AbortController();
  let opens = 0,
    closed = 0;
  // Iterators are built by hand: the sources neither await nor (mostly) yield,
  // which generator linting would reject.
  const open = (): AsyncIterable<number> => ({
    [Symbol.asyncIterator]: () => {
      opens++;
      let yielded = false;
      return {
        next: () => {
          if (opens === 1) {
            closed++;
            return Promise.reject(new WsStatusError(code, 'interrupted'));
          }
          if (yielded) return Promise.resolve({ value: undefined, done: true });
          yielded = true;
          return Promise.resolve({ value: opens, done: false });
        },
        return: () => {
          closed++;
          return Promise.resolve({ value: undefined, done: true });
        },
      };
    },
  });
  const retry = vi.fn();
  const watch = watchWithRetry(open, { signal: controller.signal, onRetry: retry, retryDelay: () => 100 })[
    Symbol.asyncIterator
  ]();
  const first = watch.next();
  await vi.advanceTimersByTimeAsync(100);
  expect(await first).toEqual({ value: 2, done: false });
  expect(retry).toHaveBeenCalledTimes(1);
  await watch.return?.();
  expect(closed).toBe(2);
});

test('abort interrupts backoff without opening another stream', async () => {
  vi.useFakeTimers();
  let opens = 0;
  const controller = new AbortController();
  const open = (): AsyncIterable<never> => ({
    [Symbol.asyncIterator]: () => {
      opens++;
      return { next: () => Promise.reject(new WsStatusError(14, 'offline')) };
    },
  });
  const next = watchWithRetry(open, { signal: controller.signal, retryDelay: () => 30_000 })
    [Symbol.asyncIterator]()
    .next();
  await vi.advanceTimersByTimeAsync(1);
  controller.abort();
  expect(await next).toMatchObject({ done: true });
  expect(opens).toBe(1);
  expect(vi.getTimerCount()).toBe(0);
});

test.each([3, 7, 11, 16])('permanent error %i is explicit and never retried', async (code) => {
  const failure = new WsStatusError(code, 'terminal');
  const open = (): AsyncIterable<never> => ({
    [Symbol.asyncIterator]: () => ({ next: () => Promise.reject(failure) }),
  });
  const retry = vi.fn();
  await expect(
    watchWithRetry(open, { signal: new AbortController().signal, onRetry: retry })[Symbol.asyncIterator]().next(),
  ).rejects.toBe(failure);
  expect(retry).not.toHaveBeenCalled();
});

test('unexpected clean EOF retries a long-lived watch', async () => {
  vi.useFakeTimers();
  let opens = 0;
  const open = (): AsyncIterable<string> => ({
    [Symbol.asyncIterator]: () => {
      opens++;
      return {
        next: () => Promise.resolve(opens > 1 ? { value: 'recovered', done: false } : { value: undefined, done: true }),
      };
    },
  });
  const watch = watchWithRetry(open, { signal: new AbortController().signal, retryDelay: () => 100 })[
    Symbol.asyncIterator
  ]();
  const next = watch.next();
  await vi.advanceTimersByTimeAsync(100);
  expect(await next).toMatchObject({ value: 'recovered' });
  await watch.return?.();
});
