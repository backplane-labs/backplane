import type { SpanRecord } from './types.js';
export function nanos(value: string): bigint | null {
  if (!/^\d{1,40}$/.test(value)) return null;
  return BigInt(value);
}
export function duration(start: string, end: string): string {
  const from = nanos(start),
    to = nanos(end);
  return from === null || to === null || to < from ? '—' : `${to - from} ns`;
}
/** Readable span length keeping sub-microsecond precision: 850 ns, 12.4 µs, 3.21 ms, 1.50 s. */
export function humanDuration(nanoseconds: bigint): string {
  if (nanoseconds < 1000n) return `${nanoseconds} ns`;
  const value = Number(nanoseconds);
  if (value < 1e6) return `${(value / 1e3).toFixed(value < 1e4 ? 2 : 1)} µs`;
  if (value < 1e9) return `${(value / 1e6).toFixed(value < 1e7 ? 2 : 1)} ms`;
  return `${(value / 1e9).toFixed(2)} s`;
}
export function nanoDate(value: string, timeZone = 'UTC'): string {
  const ns = nanos(value);
  if (ns === null) return value;
  const ms = Number(ns / 1000000n);
  if (!Number.isFinite(ms) || ms > 8640000000000000) return value;
  return `${new Intl.DateTimeFormat('en', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }).format(ms)}.${String(ns % 1000000000n).padStart(9, '0')} ${timeZone}`;
}
export interface SpanNode extends SpanRecord {
  children: SpanNode[];
  depth: number;
}
/** Iterative traversal with deterministic repair; never hand a cyclic tree to a renderer. */
export function spanForest(spans: readonly SpanRecord[]) {
  const nodes = new Map<string, SpanNode>();
  let malformed = false,
    invalidTime = false;
  for (const span of spans) {
    if (nodes.has(span.id)) {
      malformed = true;
      continue;
    }
    nodes.set(span.id, { ...span, children: [], depth: 0 });
  }
  const parents = new Map<string, string>();
  for (const node of nodes.values()) {
    if (!node.parentId) continue;
    if (!nodes.has(node.parentId) || nodes.get(node.parentId)!.traceId !== node.traceId) {
      malformed = true;
      continue;
    }
    parents.set(node.id, node.parentId);
  }
  const done = new Set<string>();
  for (const node of nodes.values()) {
    const path = new Set<string>();
    let current: string | undefined = node.id;
    while (current && !done.has(current)) {
      if (path.has(current)) {
        parents.delete(current);
        malformed = true;
        break;
      }
      path.add(current);
      current = parents.get(current);
    }
    path.forEach((id) => done.add(id));
  }
  const roots: SpanNode[] = [];
  let start: bigint | null = null,
    end: bigint | null = null;
  for (const node of nodes.values()) {
    const parent = parents.get(node.id);
    if (parent) nodes.get(parent)!.children.push(node);
    else roots.push(node);
    const from = nanos(node.startUnixNano),
      to = nanos(node.endUnixNano);
    if (from === null || to === null || to < from) {
      invalidTime = true;
      continue;
    }
    if (start === null || from < start) start = from;
    if (end === null || to > end) end = to;
  }
  const stack = roots.map((node) => ({ node, depth: 0 }));
  while (stack.length) {
    const { node, depth } = stack.pop()!;
    node.depth = depth;
    if (depth >= 64 && node.children.length) {
      malformed = true;
      for (const child of node.children) {
        roots.push(child);
        stack.push({ node: child, depth: 0 });
      }
      node.children = [];
    } else for (const child of node.children) stack.push({ node: child, depth: depth + 1 });
  }
  return { roots, start, end, malformed, invalidTime };
}
export function spanPosition(span: SpanRecord, start: bigint | null, end: bigint | null) {
  const from = nanos(span.startUnixNano),
    to = nanos(span.endUnixNano);
  if (from === null || to === null || start === null || end === null || end <= start || to < from)
    return { left: 0, width: 0 };
  const ratio = (value: bigint) => Math.max(0, Math.min(100, Number((value * 10000n) / (end - start)) / 100));
  return { left: ratio(from - start), width: ratio(to - from) };
}
