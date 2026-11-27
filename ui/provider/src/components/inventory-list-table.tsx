import { Button } from '@datum-cloud/datum-ui/button';
import { DataTable, DataTableColumnHeader, useDataTablePagination, useDataTableRows, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import type { ColumnDef, Row, RowData } from '@tanstack/react-table';
import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useEffect, useLayoutEffect, useMemo, useRef, type ReactNode, type RefObject } from 'react';
import { FieldSelect } from './field-select';
import './physical-inventory.css';

export type InventoryListColumn<T> = {
  id: string;
  header: string;
  cell: (row: T) => ReactNode;
  /** Plain value the column sorts by. */
  sort: (row: T) => string | number;
};

const PAGE_SIZES = [20, 50, 100];

/**
 * The inventory list in staff-portal's list style: a datum-ui DataTable at
 * compact density (uppercase sortable headers, dense rows) and the host's
 * "rows per page · 1–N of M" footer. Search and filters stay with the caller,
 * which keeps them in the URL; this table only sorts, pages and renders the
 * rows it is given. Callers key it by record type: DataTable.Client adopts new
 * rows a render after new columns, so one instance must never switch types.
 */
export function InventoryListTable<T extends RowData>({ label, rows, total, columns, rowKey, onOpen, empty, resource, pagination = true, rowMeta }: {
  label: string;
  rows: T[];
  total: number;
  columns: InventoryListColumn<T>[];
  rowKey: (row: T) => string;
  onOpen: (row: T) => void;
  empty: ReactNode;
  resource: string;
  /** Embedded tables that always fit one page leave the footer out. */
  pagination?: boolean;
  /**
   * Per-row marks: `attention` gives the row a coloured edge (red for a
   * conflict, amber for unavailable data) and `title` is its tooltip, set on
   * the row element as `data-attention` and `title`.
   */
  rowMeta?: (row: T) => { attention?: 'conflict' | 'unavailable'; title?: string };
}) {
  const definitions = useMemo<ColumnDef<DataTableFeatures, T, unknown>[]>(() => columns.map(column => ({
    id: column.id,
    accessorFn: (row: T) => column.sort(row),
    sortingFn: (left: Row<DataTableFeatures, T>, right: Row<DataTableFeatures, T>) => {
      const a = column.sort(left.original);
      const b = column.sort(right.original);
      return typeof a === 'number' && typeof b === 'number' ? a - b : String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' });
    },
    header: ({ column: state }) => <DataTableColumnHeader column={state as never} title={column.header} density="compact" />,
    cell: ({ row }) => column.cell(row.original),
  })), [columns]);
  const table = useRef<HTMLDivElement>(null);
  return <DataTable.Client data={rows} columns={definitions} getRowId={rowKey} pageSize={50} className="inventory-list-table-root">
    <div ref={table} className="inventory-list-table" data-testid="fleet-plugin-inventory-table">
      <DataTable.Content density="compact" emptyMessage={empty} rowClassName="inventory-row-link" onRowClick={row => { if (!window.getSelection()?.toString()) onOpen(row as T); }} />
      <TableName container={table} label={label} rowMeta={rowMeta} />
    </div>
    {pagination && total > 0 && <InventoryPagination total={total} resource={resource} />}
  </DataTable.Client>;
}

/**
 * DataTable.Content takes no table label. This sits after it inside the table
 * context, so it re-renders with the rows and its layout effect runs in the
 * same commit that (re)renders the table.
 */
function TableName<T extends RowData>({ container, label, rowMeta }: { container: RefObject<HTMLDivElement | null>; label: string; rowMeta?: (row: T) => { attention?: string; title?: string } }) {
  // DataTable.Content renders one <tr> per row of the current page, in order,
  // and takes no per-row attributes, so the row marks are set on them here.
  const { rows } = useDataTableRows<T>();
  useLayoutEffect(() => {
    const table = container.current?.querySelector('table');
    if (!table) return;
    if (table.getAttribute('aria-label') !== label) table.setAttribute('aria-label', label);
    if (!rowMeta) return;
    const elements = [...table.querySelectorAll<HTMLTableRowElement>('tbody > tr')];
    if (elements.length !== rows.length) return;
    elements.forEach((element, index) => {
      const meta = rowMeta(rows[index].original);
      if (meta.attention) element.setAttribute('data-attention', meta.attention); else element.removeAttribute('data-attention');
      if (meta.title) element.setAttribute('title', meta.title); else element.removeAttribute('title');
    });
  });
  return null;
}

function InventoryPagination({ total, resource }: { total: number; resource: string }) {
  const pagination = useDataTablePagination();
  const { canNextPage, canPrevPage, nextPage, prevPage, pageIndex, pageSize, setPageSize, totalRows } = pagination;
  const pageCount = pagination.mode === 'client' ? pagination.pageCount : undefined;
  const setPageIndex = pagination.mode === 'client' ? pagination.setPageIndex : undefined;
  // A narrower search or filter can leave the current page past the end.
  useEffect(() => { if (pageCount !== undefined && pageIndex > 0 && pageIndex >= pageCount) setPageIndex?.(Math.max(0, pageCount - 1)); }, [pageCount, pageIndex, setPageIndex]);
  const start = totalRows === 0 ? 0 : pageIndex * pageSize + 1;
  const end = Math.min((pageIndex + 1) * pageSize, totalRows);
  return <div className="inventory-pagination" data-slot="dt-pagination">
    <div className="inventory-pagination-size">
      <FieldSelect aria-label="Rows per page" className="inventory-pagination-select" value={String(pageSize)} onValueChange={value => setPageSize(Number(value))} options={PAGE_SIZES.map(size => ({ value: String(size), label: String(size) }))} />
      <span className="inventory-muted">Rows per page</span>
    </div>
    <div className="inventory-pagination-pages">
      <span className="inventory-muted inventory-pagination-range">{start}–{end} of {totalRows} {resource}{totalRows !== total ? ` · ${total} total` : ''}</span>
      <div className="inventory-pagination-buttons">
        <Button type="secondary" theme="outline" size="icon" htmlType="button" aria-label="Previous page" disabled={!canPrevPage} onClick={prevPage} icon={<ChevronLeft size={14} aria-hidden="true" />} />
        <Button type="secondary" theme="outline" size="icon" htmlType="button" aria-label="Next page" disabled={!canNextPage} onClick={nextPage} icon={<ChevronRight size={14} aria-hidden="true" />} />
      </div>
    </div>
  </div>;
}
