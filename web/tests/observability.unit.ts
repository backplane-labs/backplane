import { describe, expect, it } from 'vitest';
import { duration, nanoDate, spanForest, spanPosition } from '../packages/observability-ui/src/model';
import type { SpanRecord } from '../packages/observability-ui/src/types';

const span = (id: string, parentId?: string): SpanRecord => ({
  id,
  parentId,
  traceId: 'trace',
  name: id,
  startUnixNano: '1790683200123456789',
  endUnixNano: '1790683200123456799',
});
describe('trace data boundaries', () => {
  it('preserves nanoseconds through formatting and relative positions', () => {
    expect(duration(span('a').startUnixNano, span('a').endUnixNano)).toBe('10 ns');
    expect(nanoDate(span('a').startUnixNano)).toContain('.123456789 UTC');
    expect(spanPosition(span('a'), 1790683200123456780n, 1790683200123456800n)).toEqual({ left: 45, width: 50 });
    expect(duration('10', '9')).toBe('—');
  });
  it('repairs missing parents, duplicate IDs and cycles without dropping unique spans', () => {
    const forest = spanForest([span('a', 'b'), span('b', 'a'), span('c', 'missing'), span('a')]);
    expect(forest.malformed).toBe(true);
    const ids = new Set<string>(),
      queue = [...forest.roots];
    while (queue.length) {
      const node = queue.pop()!;
      expect(ids.has(node.id)).toBe(false);
      ids.add(node.id);
      queue.push(...node.children);
    }
    expect([...ids].sort()).toEqual(['a', 'b', 'c']);
  });
  it('bounds tree recursion depth for hostile long parent chains', () => {
    const forest = spanForest(
      Array.from({ length: 10000 }, (_, index) => span(String(index), index ? String(index - 1) : undefined)),
    );
    const queue = [...forest.roots];
    let count = 0;
    while (queue.length) {
      const node = queue.pop()!;
      expect(node.depth).toBeLessThanOrEqual(64);
      count++;
      queue.push(...node.children);
    }
    expect(count).toBe(10000);
    expect(forest.malformed).toBe(true);
  });
});
