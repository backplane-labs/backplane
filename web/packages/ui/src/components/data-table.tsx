import {
  type ColumnDef,
  type ColumnFiltersState,
  type ColumnPinningState,
  type ExpandedState,
  flexRender,
  getCoreRowModel,
  getExpandedRowModel,
  getFilteredRowModel,
  getPaginationRowModel,
  getSortedRowModel,
  type RowSelectionState,
  type SortingState,
  useReactTable,
  type VisibilityState,
} from '@tanstack/react-table';
import { useVirtualizer } from '@tanstack/react-virtual';
import { type ReactNode, useCallback, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from './button.js';
import { Checkbox } from './checkbox.js';
import { Input } from './input.js';
import { Popover, PopoverContent, PopoverTrigger } from './popover.js';
import { SelectControl } from './select-control.js';

export interface DataColumn<T> {
  id: string;
  label: string;
  value: (row: T) => string | number | bigint | boolean | null | undefined;
  render?: (row: T) => ReactNode;
  width?: number;
  sortable?: boolean;
  filterable?: boolean;
}
export interface DataTableProps<T> {
  label: string;
  data: T[];
  columns: readonly DataColumn<T>[];
  getRowId: (row: T) => string;
  getChildren?: (row: T) => T[] | undefined;
  height?: number;
  pageSize?: number;
  selectable?: boolean;
  onSelectionChange?: (ids: string[]) => void;
  onActivate?: (row: T) => void;
  loading?: boolean;
  error?: string;
  partial?: boolean;
}

export function DataTable<T>({
  label,
  data,
  columns: definitions,
  getRowId,
  getChildren,
  height = 400,
  pageSize,
  selectable,
  onSelectionChange,
  onActivate,
  loading,
  error,
  partial,
}: DataTableProps<T>) {
  const { t } = useTranslation('backplane.ui');
  const [sorting, setSorting] = useState<SortingState>([]),
    [filters, setFilters] = useState<ColumnFiltersState>([]);
  const [visibility, setVisibility] = useState<VisibilityState>({}),
    [order, setOrder] = useState<string[]>([]);
  const [pinning, setPinning] = useState<ColumnPinningState>({}),
    [selection, setSelection] = useState<RowSelectionState>({});
  const [expanded, setExpanded] = useState<ExpandedState>({}),
    [query, setQuery] = useState(''),
    [focused, setFocused] = useState<string>();
  const [showFilters, setShowFilters] = useState(false);
  const headerHeight = showFilters ? 66 : 34;
  const columns = useMemo<ColumnDef<T>[]>(
    () =>
      definitions.map((definition) => ({
        id: definition.id,
        header: definition.label,
        accessorFn: definition.value,
        size: definition.width ?? 180,
        minSize: 80,
        enableSorting: definition.sortable !== false,
        enableColumnFilter: definition.filterable !== false,
        cell: ({ row }) =>
          definition.render ? definition.render(row.original) : String(definition.value(row.original) ?? ''),
        filterFn: (row, id, value: string) =>
          String(row.getValue(id) ?? '')
            .toLowerCase()
            .includes(value.toLowerCase()),
        sortingFn: (a, b, id) => {
          const left = a.getValue<string | number | bigint | boolean | null>(id),
            right = b.getValue<typeof left>(id);
          if (left === right) return 0;
          if (left == null) return -1;
          if (right == null) return 1;
          return left < right ? -1 : 1;
        },
      })),
    [definitions],
  );
  const table = useReactTable({
    data,
    columns,
    getRowId,
    getSubRows: getChildren,
    state: {
      sorting,
      columnFilters: filters,
      columnVisibility: visibility,
      columnOrder: order,
      columnPinning: pinning,
      rowSelection: selection,
      expanded,
      globalFilter: query,
    },
    onSortingChange: setSorting,
    onColumnFiltersChange: setFilters,
    onColumnVisibilityChange: setVisibility,
    onColumnOrderChange: setOrder,
    onColumnPinningChange: setPinning,
    onExpandedChange: setExpanded,
    onGlobalFilterChange: setQuery,
    onRowSelectionChange: (updater) => {
      const next = typeof updater === 'function' ? updater(selection) : updater;
      setSelection(next);
      onSelectionChange?.(Object.keys(next).filter((key) => next[key]));
    },
    enableRowSelection: !!selectable,
    columnResizeMode: 'onChange',
    getColumnCanGlobalFilter: () => true,
    globalFilterFn: (row, id, value: string) =>
      String(row.getValue(id) ?? '')
        .toLowerCase()
        .includes(value.toLowerCase()),
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    getExpandedRowModel: getExpandedRowModel(),
    getPaginationRowModel: pageSize ? getPaginationRowModel() : undefined,
    initialState: { pagination: { pageIndex: 0, pageSize: pageSize ?? 100 } },
  });
  const rows = table.getRowModel().rows;
  const scroll = useRef<HTMLDivElement>(null);
  const key = useCallback((index: number) => rows[index].id, [rows]);
  const virtual = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scroll.current,
    estimateSize: () => 36,
    overscan: 6,
    getItemKey: key,
    scrollMargin: headerHeight,
  });
  const rowWidth = table.getTotalSize() + (selectable ? 44 : 0) + (getChildren ? 44 : 0);
  const reorder = (id: string, delta: number) => {
    const current = table.getAllLeafColumns().map((column) => column.id);
    const at = current.indexOf(id);
    const next = at + delta;
    if (next >= 0 && next < current.length) {
      [current[at], current[next]] = [current[next], current[at]];
      setOrder(current);
    }
  };
  const focusRow = (index: number) => {
    const row = rows[index];
    if (!row) return;
    setFocused(row.id);
    virtual.scrollToIndex(index);
    requestAnimationFrame(() =>
      scroll.current?.querySelector<HTMLElement>(`[data-row-id="${CSS.escape(row.id)}"]`)?.focus(),
    );
  };
  const pinnedStyle = (column: ReturnType<typeof table.getAllLeafColumns>[number]) => {
    const pinned = column.getIsPinned();
    return {
      width: column.getSize(),
      minWidth: column.getSize(),
      position: pinned ? ('sticky' as const) : ('relative' as const),
      left: pinned === 'left' ? column.getStart('left') : undefined,
      right: pinned === 'right' ? column.getAfter('right') : undefined,
      zIndex: pinned ? 2 : undefined,
      background: pinned ? 'var(--card)' : undefined,
    };
  };
  return (
    <section aria-label={label} className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <Input
          className="h-7 w-64 text-xs"
          type="search"
          placeholder={t('search')}
          aria-label={t('filterTable', { name: label })}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <Button
          type="button"
          size="sm"
          variant={showFilters ? 'secondary' : 'ghost'}
          aria-pressed={showFilters}
          onClick={() => setShowFilters((value) => !value)}
        >
          {t('filters')}
        </Button>
        <Popover>
          <PopoverTrigger asChild>
            <Button type="button" size="sm" variant="ghost">
              {t('columns')}
            </Button>
          </PopoverTrigger>
          <PopoverContent className="w-96">
            <div className="flex flex-col gap-3">
              {table.getAllLeafColumns().map((column, index, all) => (
                <div key={column.id} className="flex items-center gap-2">
                  <Checkbox
                    aria-label={t('showColumn', { name: String(column.columnDef.header) })}
                    checked={column.getIsVisible()}
                    disabled={column.getIsVisible() && table.getVisibleLeafColumns().length === 1}
                    onCheckedChange={(value) => column.toggleVisibility(value === true)}
                  />
                  <span>{String(column.columnDef.header)}</span>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t('moveLeft', { name: String(column.columnDef.header) })}
                    disabled={index === 0}
                    onClick={() => reorder(column.id, -1)}
                  >
                    ←
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t('moveRight', { name: String(column.columnDef.header) })}
                    disabled={index === all.length - 1}
                    onClick={() => reorder(column.id, 1)}
                  >
                    →
                  </Button>
                  <SelectControl
                    aria-label={t('pinColumn', { name: String(column.columnDef.header) })}
                    value={column.getIsPinned() || ''}
                    onValueChange={(value) =>
                      column.pin(value === 'left' ? 'left' : value === 'right' ? 'right' : false)
                    }
                    options={[
                      { value: '', label: t('unpinned') },
                      { value: 'left', label: t('pinLeft') },
                      { value: 'right', label: t('pinRight') },
                    ]}
                  />
                </div>
              ))}
            </div>
          </PopoverContent>
        </Popover>
      </div>
      {loading && (
        <p role="status" className="m-0 text-xs text-muted-foreground">
          {t('loading')}
        </p>
      )}
      {error && (
        <p role="alert" className="m-0 text-xs text-destructive">
          {error}
        </p>
      )}
      {partial && (
        <p role="status" className="m-0 text-xs text-warning">
          {t('partialResults')}
        </p>
      )}
      <div
        ref={scroll}
        style={{ height, overflow: 'auto' }}
        className="rounded-lg border border-border bg-card text-sm"
      >
        <table
          aria-label={label}
          aria-rowcount={rows.length + 1}
          style={{ display: 'grid', width: rowWidth, minWidth: '100%' }}
        >
          <thead style={{ display: 'grid', position: 'sticky', top: 0, zIndex: 3 }} className="bg-raised">
            {table.getHeaderGroups().map((group) => (
              <tr
                key={group.id}
                style={{ display: 'flex', minHeight: headerHeight }}
                className="border-b border-border"
              >
                {selectable && (
                  <th style={{ width: 44, padding: '8px 12px' }}>
                    <Checkbox
                      aria-label={t('selectAllRows')}
                      checked={
                        table.getIsAllRowsSelected() || (table.getIsSomeRowsSelected() ? 'indeterminate' : false)
                      }
                      onCheckedChange={(value) => table.toggleAllRowsSelected(value === true)}
                    />
                  </th>
                )}
                {getChildren && (
                  <th style={{ width: 44 }}>
                    <span className="sr-only">{t('expand')}</span>
                  </th>
                )}
                {group.headers.map((header) => (
                  <th
                    key={header.id}
                    aria-sort={
                      header.column.getIsSorted() === 'asc'
                        ? 'ascending'
                        : header.column.getIsSorted() === 'desc'
                          ? 'descending'
                          : 'none'
                    }
                    style={{ ...pinnedStyle(header.column), textAlign: 'left', padding: '6px 12px' }}
                    className="text-xs font-medium text-muted-foreground"
                  >
                    <button
                      type="button"
                      disabled={!header.column.getCanSort()}
                      onClick={header.column.getToggleSortingHandler()}
                      className="inline-flex h-5 items-center gap-1 font-medium whitespace-nowrap hover:text-foreground disabled:hover:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      {flexRender(header.column.columnDef.header, header.getContext())}
                      {header.column.getIsSorted() === 'asc'
                        ? ' ↑'
                        : header.column.getIsSorted() === 'desc'
                          ? ' ↓'
                          : ''}
                    </button>
                    {showFilters && header.column.getCanFilter() && (
                      <Input
                        className="mt-1 h-6 bg-background text-xs text-foreground dark:bg-background"
                        aria-label={t('filterColumn', { name: String(header.column.columnDef.header) })}
                        value={String(header.column.getFilterValue() ?? '')}
                        onChange={(event) => header.column.setFilterValue(event.target.value)}
                      />
                    )}
                    <hr
                      aria-orientation="vertical"
                      aria-label={t('resizeColumn', { name: String(header.column.columnDef.header) })}
                      aria-valuenow={header.getSize()}
                      tabIndex={0}
                      onMouseDown={header.getResizeHandler()}
                      onTouchStart={header.getResizeHandler()}
                      onKeyDown={(event) => {
                        if (['ArrowLeft', 'ArrowRight'].includes(event.key)) {
                          event.preventDefault();
                          header.column.resetSize();
                          table.setColumnSizing((old) => ({
                            ...old,
                            [header.column.id]: Math.max(80, header.getSize() + (event.key === 'ArrowLeft' ? -10 : 10)),
                          }));
                        }
                      }}
                      style={{
                        position: 'absolute',
                        top: 0,
                        right: 0,
                        width: 6,
                        height: '100%',
                        cursor: 'col-resize',
                        touchAction: 'none',
                      }}
                      className="border-0 hover:bg-primary/40 focus-visible:bg-primary/40"
                    />
                  </th>
                ))}
              </tr>
            ))}
          </thead>
          <tbody style={{ display: 'grid', height: virtual.getTotalSize(), position: 'relative' }}>
            {virtual.getVirtualItems().map((item) => {
              const row = rows[item.index];
              return (
                <tr
                  key={row.id}
                  data-row-id={row.id}
                  data-index={item.index}
                  ref={virtual.measureElement}
                  tabIndex={
                    focused === row.id || (!rows.some((row) => row.id === focused) && item.index === 0) ? 0 : -1
                  }
                  aria-selected={selectable ? row.getIsSelected() : undefined}
                  aria-rowindex={item.index + 2}
                  onClick={(event) => {
                    if (
                      event.target instanceof Element &&
                      event.target.closest(
                        'button, a, input, select, textarea, [role="checkbox"], [contenteditable="true"]',
                      )
                    )
                      return;
                    event.currentTarget.focus();
                    onActivate?.(row.original);
                  }}
                  onFocus={() => setFocused(row.id)}
                  onKeyDown={(event) => {
                    if (event.target !== event.currentTarget) return;
                    if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
                      event.preventDefault();
                      focusRow(
                        event.key === 'Home'
                          ? 0
                          : event.key === 'End'
                            ? rows.length - 1
                            : Math.max(0, Math.min(rows.length - 1, item.index + (event.key === 'ArrowDown' ? 1 : -1))),
                      );
                    }
                    if (event.key === ' ' && selectable) {
                      event.preventDefault();
                      row.toggleSelected();
                    }
                    if (event.key === 'Enter') onActivate?.(row.original);
                  }}
                  style={{
                    display: 'flex',
                    position: 'absolute',
                    transform: `translateY(${item.start - headerHeight}px)`,
                    width: '100%',
                    minHeight: 36,
                    cursor: onActivate ? 'pointer' : undefined,
                  }}
                  className="border-b border-border hover:bg-raised focus-visible:bg-raised focus-visible:outline-none aria-selected:bg-primary/10"
                >
                  {selectable && (
                    <td style={{ width: 44, padding: '8px 12px' }}>
                      <Checkbox
                        aria-label={t('selectRow', { name: row.id })}
                        checked={row.getIsSelected()}
                        onCheckedChange={(value) => row.toggleSelected(value === true)}
                      />
                    </td>
                  )}
                  {getChildren && (
                    <td style={{ width: 44 }}>
                      {row.getCanExpand() && (
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          aria-label={t(row.getIsExpanded() ? 'collapseRow' : 'expandRow', { name: row.id })}
                          onClick={row.getToggleExpandedHandler()}
                        >
                          {row.getIsExpanded() ? '−' : '+'}
                        </Button>
                      )}
                    </td>
                  )}
                  {row.getVisibleCells().map((cell) => (
                    <td
                      key={cell.id}
                      style={{
                        ...pinnedStyle(cell.column),
                        padding: '8px 12px',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                      }}
                    >
                      {flexRender(cell.column.columnDef.cell, cell.getContext())}
                    </td>
                  ))}
                </tr>
              );
            })}
          </tbody>
        </table>
        {!loading && !rows.length && <p className="m-0 p-6 text-center text-sm text-muted-foreground">{t('noRows')}</p>}
      </div>
      <div className="flex items-center gap-3 text-xs text-muted-foreground" role="status">
        <span>{t('rowCount', { count: table.getFilteredRowModel().rows.length })}</span>
        {selectable && <span>{t('selectedCount', { count: Object.values(selection).filter(Boolean).length })}</span>}
      </div>
      {pageSize && (
        <nav aria-label={t('pagination')} className="flex gap-2">
          <Button
            type="button"
            variant="outline"
            disabled={!table.getCanPreviousPage()}
            onClick={() => table.previousPage()}
          >
            {t('previous')}
          </Button>
          <span>
            {table.getState().pagination.pageIndex + 1} / {Math.max(1, table.getPageCount())}
          </span>
          <Button type="button" variant="outline" disabled={!table.getCanNextPage()} onClick={() => table.nextPage()}>
            {t('next')}
          </Button>
        </nav>
      )}
    </section>
  );
}
