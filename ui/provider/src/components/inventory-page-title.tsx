import { ArrowLeft } from 'lucide-react';
import { Link, useLocation } from 'react-router';
import { useEffect, useRef, type ComponentType, type ReactNode } from 'react';
import { isCatalogKind } from './inventory-groups';

/**
 * The list page for a record kind: Manufacturers and Device types live on the
 * Catalog page, everything else on Inventory. Without a kind, the page the
 * pathname is already on.
 */
export function inventoryDirectoryPath(pathname: string, kind?: string) {
  const match = pathname.match(/^(\/plugins\/[^/]+)\/(inventory|catalog)(?:\/|$)/);
  const plugin = match?.[1] ?? '';
  const page = kind !== undefined ? (isCatalogKind(kind) ? 'catalog' : 'inventory') : (match?.[2] ?? (/^\/catalog(?:\/|$)/.test(pathname) ? 'catalog' : 'inventory'));
  return `${plugin}/${page}`;
}

export function inventoryReturnSearch(search: string, kind: string) {
  const source = new URLSearchParams(search);
  const result = new URLSearchParams({ kind });
  for (const key of ['q', 'location', 'model']) {
    const value = source.get(key);
    if (value) result.set(key, value);
  }
  return `?${result}`;
}

// Detail and task pages follow staff-portal's EntityHeader: a back link to the
// category list (keeping its filters), then an icon tile, the record name as the
// page heading, a muted subtitle, and actions on the right. There is no
// breadcrumb: the Bare Metal tabs and the back link say where the reader is.
export function InventoryPageTitle({ title, description, actions, directory, category, focusTitle = false }: { title: string; description?: ReactNode; actions?: ReactNode; directory?: string; category?: { kind: string; title: string; icon?: ComponentType<{ size?: number; 'aria-hidden'?: boolean | 'true' }> }; focusTitle?: boolean }) {
  const { pathname } = useLocation();
  const base = inventoryDirectoryPath(pathname, category?.kind);
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => { if (focusTitle) heading.current?.focus(); }, [title, focusTitle]);
  const Icon = category?.icon;
  const back = directory ? (directory.startsWith('/') ? directory : `${base}${directory}`) : base;
  return <div className="inventory-page-chrome">
    <Link className="inventory-back-link" to={back}><ArrowLeft size={14} aria-hidden="true" />{category?.title ?? 'Inventory'}</Link>
    <div className="inventory-entity-header">
      {Icon && <div className="inventory-entity-icon" aria-hidden="true"><Icon size={20} aria-hidden="true" /></div>}
      <div className="inventory-entity-text">
        <h1 className="inventory-entity-name" tabIndex={-1} ref={heading}>{title}</h1>
        {description && <div className="inventory-entity-subtitle">{description}</div>}
      </div>
      {actions && <div className="inventory-title-actions">{actions}</div>}
    </div>
  </div>;
}
