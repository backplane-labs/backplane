import type { CapturedInput, ExceptionData, ExceptionInfo } from './protocol.js';

export interface ExceptionLimits {
  maxDepth?: number;
  maxNodes?: number;
}
export function exceptionReader(limits: ExceptionLimits = {}) {
  const depth = limits.maxDepth ?? 8,
    nodes = limits.maxNodes ?? 32;
  if (![depth, nodes].every((n) => Number.isSafeInteger(n) && n >= 1 && n <= 256))
    throw new TypeError('invalid_exception_limits');
  return (value: CapturedInput, mechanism: ExceptionData['mechanism'], handled?: boolean): ExceptionData => {
    const ancestors = new Set<object>();
    let count = 0;
    const read = (input: CapturedInput, level: number): ExceptionInfo => {
      if (level > depth || count >= nodes) return { incomplete: 'limit' };
      count++;
      if (input === null || (typeof input !== 'object' && typeof input !== 'function')) {
        return { message: String(input) };
      }
      if (ancestors.has(input)) return { incomplete: 'cycle' };
      ancestors.add(input);
      const result: { -readonly [K in keyof ExceptionInfo]: ExceptionInfo[K] } = {};
      try {
        for (const [key, dest] of [
          ['name', 'type'],
          ['message', 'message'],
          ['stack', 'stacktrace'],
        ] as const) {
          try {
            const field = Reflect.get(input, key);
            if (typeof field === 'string') result[dest] = field;
          } catch {
            result.incomplete = 'unreadable';
          }
        }
        // Only data properties: do not execute cause/errors getters or iterate arbitrary objects.
        for (const key of ['cause', 'errors'] as const) {
          try {
            const descriptor = Object.getOwnPropertyDescriptor(input, key);
            if (!descriptor) continue;
            if (!('value' in descriptor)) {
              result.incomplete = 'unreadable';
              continue;
            }
            if (key === 'cause') result.cause = read(descriptor.value, level + 1);
            else if (Array.isArray(descriptor.value)) {
              const errors: ExceptionInfo[] = [];
              for (let i = 0; i < descriptor.value.length; i++) {
                if (count >= nodes || level >= depth) {
                  result.incomplete = 'limit';
                  break;
                }
                const item = Object.getOwnPropertyDescriptor(descriptor.value, String(i));
                errors.push(item && 'value' in item ? read(item.value, level + 1) : { incomplete: 'unreadable' });
              }
              result.errors = errors;
            }
          } catch {
            result.incomplete = 'unreadable';
          }
        }
      } finally {
        ancestors.delete(input);
      }
      return result;
    };
    return { ...read(value, 0), mechanism, ...(handled === undefined ? {} : { handled }) };
  };
}
