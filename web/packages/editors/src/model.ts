import type { EditorDiagnostic } from './types.js';
/** Input offsets are UTF-16 code units, matching CodeMirror and JavaScript strings. */
export function boundDiagnostics(items: readonly EditorDiagnostic[], length: number): EditorDiagnostic[] {
  return items.slice(0, 1000).map((item) => {
    const from = Number.isFinite(item.from) ? Math.max(0, Math.min(length, Math.trunc(item.from))) : 0;
    return {
      ...item,
      from,
      to: Number.isFinite(item.to) ? Math.max(from, Math.min(length, Math.trunc(item.to))) : from,
    };
  });
}
/** Bigints stay decimal strings in JSON; callers needing typed values use protobuf JSON. */
export function displayJSON(value: unknown): string {
  const seen = new WeakSet<object>();
  return (
    JSON.stringify(
      value,
      (_key, current) => {
        if (typeof current === 'bigint') return current.toString();
        if (current !== null && typeof current === 'object') {
          if (seen.has(current)) return '[Circular or repeated reference]';
          seen.add(current);
        }
        return current;
      },
      2,
    ) ?? 'null'
  );
}
