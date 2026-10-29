/** The global scope that dispatches page and worker events: a page's window or a worker's self; undefined outside browsers. */
export function globalScope(): typeof globalThis | undefined {
  return typeof globalThis.addEventListener === 'function' ? globalThis : undefined
}
