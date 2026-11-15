import { useVirtualizer } from '@tanstack/react-virtual';
import { useCallback, useMemo, useRef, useState } from 'react';

export interface TreeNode {
  id: string;
  label: string;
  children?: readonly TreeNode[];
  disabled?: boolean;
}
export interface ResourceTreeProps {
  label: string;
  nodes: readonly TreeNode[];
  selected: readonly string[];
  onSelectionChange: (ids: string[]) => void;
  onActivate?: (node: TreeNode) => void;
  multiple?: boolean;
  height?: number;
  defaultExpanded?: readonly string[];
}
type FlatNode = { node: TreeNode; level: number; parent?: string; position: number; siblings: number };

export function ResourceTree({
  label,
  nodes,
  selected,
  onSelectionChange,
  onActivate,
  multiple,
  height = 360,
  defaultExpanded = [],
}: ResourceTreeProps) {
  const [expanded, setExpanded] = useState(() => new Set(defaultExpanded)),
    [focused, setFocused] = useState<string>();
  const flat = useMemo(() => {
    const result: FlatNode[] = [];
    const pending = nodes
      .map((node, index): FlatNode => ({ node, level: 1, position: index + 1, siblings: nodes.length }))
      .reverse();
    const seen = new Set<string>();
    while (pending.length) {
      const entry = pending.pop();
      if (!entry) break;
      if (seen.has(entry.node.id)) throw new Error('ResourceTree requires unique node IDs and an acyclic hierarchy');
      seen.add(entry.node.id);
      result.push(entry);
      if (expanded.has(entry.node.id)) {
        const children = entry.node.children ?? [];
        for (let index = children.length - 1; index >= 0; index--)
          pending.push({
            node: children[index],
            parent: entry.node.id,
            level: entry.level + 1,
            position: index + 1,
            siblings: children.length,
          });
      }
    }
    return result;
  }, [nodes, expanded]);
  const scroll = useRef<HTMLDivElement>(null);
  const key = useCallback((index: number) => flat[index].node.id, [flat]);
  const virtual = useVirtualizer({
    count: flat.length,
    getScrollElement: () => scroll.current,
    estimateSize: () => 36,
    overscan: 6,
    getItemKey: key,
  });
  const search = useRef({ text: '', time: 0 });
  const toggle = (id: string) =>
    setExpanded((old) => {
      const next = new Set(old);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const select = (node: TreeNode) => {
    if (!node.disabled)
      onSelectionChange(
        multiple
          ? selected.includes(node.id)
            ? selected.filter((id) => id !== node.id)
            : [...selected, node.id]
          : [node.id],
      );
  };
  const focus = (index: number) => {
    const target = flat[index];
    if (!target) return;
    setFocused(target.node.id);
    virtual.scrollToIndex(index);
    requestAnimationFrame(() =>
      scroll.current?.querySelector<HTMLElement>(`[data-node-id="${CSS.escape(target.node.id)}"]`)?.focus(),
    );
  };
  const focusExists = flat.some((entry) => entry.node.id === focused);
  return (
    <div
      ref={scroll}
      role="tree"
      aria-label={label}
      aria-multiselectable={multiple || undefined}
      style={{ height, overflow: 'auto' }}
      className="rounded-md border"
    >
      <div role="presentation" style={{ height: virtual.getTotalSize(), position: 'relative' }}>
        {virtual.getVirtualItems().map((item) => {
          const { node, parent, level, position, siblings } = flat[item.index];
          return (
            <div
              key={node.id}
              data-node-id={node.id}
              role="treeitem"
              aria-label={node.label}
              aria-level={level}
              aria-posinset={position}
              aria-setsize={siblings}
              aria-expanded={node.children?.length ? expanded.has(node.id) : undefined}
              aria-selected={selected.includes(node.id)}
              aria-disabled={node.disabled || undefined}
              tabIndex={focused === node.id || (!focusExists && item.index === 0) ? 0 : -1}
              style={{
                position: 'absolute',
                transform: `translateY(${item.start}px)`,
                minWidth: '100%',
                height: item.size,
                padding: `6px 12px 6px ${12 + (level - 1) * 20}px`,
                whiteSpace: 'nowrap',
                background: selected.includes(node.id) ? 'var(--accent)' : undefined,
                opacity: node.disabled ? 0.5 : undefined,
              }}
              className="cursor-default focus-visible:outline-2 focus-visible:outline-ring"
              onFocus={() => setFocused(node.id)}
              onClick={() => {
                select(node);
                setFocused(node.id);
              }}
              onDoubleClick={() => {
                if (node.children?.length) toggle(node.id);
              }}
              onKeyDown={(event) => {
                const index = item.index;
                switch (event.key) {
                  case 'ArrowDown':
                    event.preventDefault();
                    focus(Math.min(flat.length - 1, index + 1));
                    break;
                  case 'ArrowUp':
                    event.preventDefault();
                    focus(Math.max(0, index - 1));
                    break;
                  case 'Home':
                    event.preventDefault();
                    focus(0);
                    break;
                  case 'End':
                    event.preventDefault();
                    focus(flat.length - 1);
                    break;
                  case 'ArrowRight':
                    event.preventDefault();
                    if (node.children?.length) {
                      if (!expanded.has(node.id)) toggle(node.id);
                      else focus(index + 1);
                    }
                    break;
                  case 'ArrowLeft':
                    event.preventDefault();
                    if (expanded.has(node.id)) toggle(node.id);
                    else if (parent) focus(flat.findIndex((entry) => entry.node.id === parent));
                    break;
                  case ' ':
                    event.preventDefault();
                    select(node);
                    break;
                  case 'Enter':
                    event.preventDefault();
                    if (!node.disabled) {
                      if (onActivate) onActivate(node);
                      else select(node);
                    }
                    break;
                  default:
                    if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
                      search.current.text =
                        (Date.now() - search.current.time < 800 ? search.current.text : '') + event.key.toLowerCase();
                      search.current.time = Date.now();
                      const candidates = [...flat.slice(index + 1), ...flat.slice(0, index + 1)];
                      const match = candidates.find((entry) =>
                        entry.node.label.toLowerCase().startsWith(search.current.text),
                      );
                      if (match) focus(flat.indexOf(match));
                    }
                }
              }}
            >
              {node.children?.length ? (
                <span
                  aria-hidden="true"
                  onClick={(event) => {
                    event.stopPropagation();
                    toggle(node.id);
                  }}
                  className="mr-2"
                >
                  {expanded.has(node.id) ? '▾' : '▸'}
                </span>
              ) : (
                <span aria-hidden="true" className="mr-2">
                  ·
                </span>
              )}
              {node.label}
            </div>
          );
        })}
      </div>
    </div>
  );
}
