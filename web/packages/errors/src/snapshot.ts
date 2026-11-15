import type { DebugSnapshotV1, SourceSnapshot } from './protocol.js';

const encoder = new TextEncoder();
const bytes = (value: unknown) => encoder.encode(JSON.stringify(value)).byteLength;

/** Budget a detached DTO only; never modify retained history or source snapshots. */
export function budgetSnapshot(
  input: DebugSnapshotV1,
  options: { maxBytes?: number; maxHistoryEntries?: number },
): DebugSnapshotV1 {
  // SDK-owned optional fields (e.g. history trace) can be undefined. JSON removes
  // those fields exactly as capture's Body does; user data was already materialized.
  const copy = JSON.parse(JSON.stringify(input)) as DebugSnapshotV1;
  const value = {
    ...copy,
    state: { sources: [...copy.state.sources] },
    history: { ...copy.history, items: [...copy.history.items] },
    diagnostics: [...(copy.diagnostics ?? [])],
  };
  function limit(requested: number | undefined, fallback: number) {
    if (requested === undefined) return fallback;
    if (Number.isSafeInteger(requested) && requested >= 0) return requested;
    if (!value.diagnostics.some((d) => d.code === 'snapshot_options'))
      value.diagnostics.push({ code: 'snapshot_options', stage: 'capture' });
    return fallback;
  }
  const maxBytes = limit(options.maxBytes, 64 * 1024);
  const maxEntries = limit(options.maxHistoryEntries, value.history.items.length);
  const dropped = Math.max(0, value.history.items.length - maxEntries);
  value.history.items = value.history.items.slice(dropped);
  value.history.truncatedCount = dropped;
  if (bytes(value) <= maxBytes) return value;

  value.diagnostics.push({ code: 'snapshot_budget', stage: 'serialize' });
  const items = value.history.items;
  // Find the smallest oldest prefix to omit; avoid serializing an entire large
  // history once per entry. Include the diagnostic and truncatedCount in the budget.
  let low = 0,
    high = items.length;
  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    value.history.items = items.slice(middle);
    value.history.truncatedCount = dropped + middle;
    if (bytes(value) <= maxBytes) high = middle;
    else low = middle + 1;
  }
  value.history.items = items.slice(low);
  value.history.truncatedCount = dropped + low;
  let size = bytes(value);
  if (size <= maxBytes) return value;

  const ranked = value.state.sources
    .flatMap((source, index) => (source.status === 'ok' ? [{ index, size: bytes(source.value) }] : []))
    .sort((a, b) => b.size - a.size || a.index - b.index);
  for (const { index } of ranked) {
    const source = value.state.sources[index];
    const replacement: SourceSnapshot = {
      name: source.name,
      registrationId: source.registrationId,
      capturedAtUnixNano: source.capturedAtUnixNano,
      monotonicMs: source.monotonicMs,
      status: 'error',
      error: { code: 'budget_exceeded', stage: 'serialize' },
    };
    size += bytes(replacement) - bytes(source);
    value.state.sources[index] = replacement;
    if (size <= maxBytes) break;
  }
  // Required identity/metadata and error markers cannot always fit (e.g. maxBytes=0).
  // Preserve the contract and the explicit budget diagnostic instead of throwing.
  return value;
}
