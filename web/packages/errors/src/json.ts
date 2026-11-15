import type { CapturedInput, JsonValue } from './protocol.js';

/** Copies data descriptors only. No JSON round-trip, getters, coercion or toJSON. */
export function materialize(input: CapturedInput): JsonValue {
  const ancestors = new Set<object>();
  function copy(value: CapturedInput): JsonValue {
    if (value === null || typeof value === 'string' || typeof value === 'boolean') return value;
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value !== 'object' || value === null) throw new TypeError('not_json');
    if (ancestors.has(value)) throw new TypeError('cyclic_json');
    const array = Array.isArray(value) ? value : undefined;
    const proto = Object.getPrototypeOf(value);
    if (!array && proto !== Object.prototype && proto !== null) throw new TypeError('not_plain_json');
    ancestors.add(value);
    try {
      const descriptors = Object.getOwnPropertyDescriptors(value);
      const ownKeys = Reflect.ownKeys(descriptors);
      const keys: string[] = [];
      for (const key of ownKeys) {
        if (typeof key !== 'string') throw new TypeError('symbol_key');
        keys.push(key);
      }
      const result: Record<string, JsonValue> = Object.create(null);
      const list: JsonValue[] = [];
      for (const key of keys) {
        if (array && key === 'length') continue;
        const d = descriptors[key];
        if (!('value' in d) || !d.enumerable) throw new TypeError('not_data_property');
        if (array && (!/^(0|[1-9][0-9]*)$/.test(key) || Number(key) >= array.length))
          throw new TypeError('array_property');
        const item = copy(d.value);
        if (array) list[Number(key)] = item;
        else
          Object.defineProperty(result, key, {
            value: item,
            enumerable: true,
            writable: true,
            configurable: true,
          });
      }
      if (array && keys.length - 1 !== array.length) throw new TypeError('sparse_array');
      return array ? list : result;
    } finally {
      ancestors.delete(value);
    }
  }
  return copy(input);
}
