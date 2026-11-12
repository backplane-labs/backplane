import type { Runtime } from '../packages/errors/src/ports'

// Node's clock and ids, read at call time so tests can spy on performance.now.
export const nodeRuntime: Runtime = {
  now: () => Date.now(),
  monotonic: () => performance.now(),
  randomId: () => crypto.randomUUID(),
}
