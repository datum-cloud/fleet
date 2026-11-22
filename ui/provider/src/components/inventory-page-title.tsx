import { Breadcrumb, BreadcrumbItem, BreadcrumbLink, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from '@datum-cloud/datum-ui/breadcrumb';
import { PageTitle } from '@datum-cloud/datum-ui/page-title';
import { Link, useLocation } from 'react-router';
import { useEffect, useRef, type ReactNode } from 'react';
import { inventoryGroup } from './inventory-groups';

export function inventoryDirectoryPath(pathname: string) {
  const plugin = pathname.match(/^(\/plugins\/[^/]+)\/inventory(?:\/|$)/)?.[1] ?? '';
  return `${plugin}/inventory`;
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

export function InventoryPageTitle({ title, description, actions, directory, category, focusTitle = false }: { title: string; description?: ReactNode; actions?: ReactNode; directory?: string; category?: { kind: string; title: string }; focusTitle?: boolean }) {
  const { pathname } = useLocation();
  const base = inventoryDirectoryPath(pathname);
  const group = category ? inventoryGroup(category.kind) : undefined;
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => { if (focusTitle) heading.current?.focus(); }, [title, focusTitle]);
  return <div className="inventory-page-chrome">
    <Breadcrumb><BreadcrumbList>
      <BreadcrumbItem><BreadcrumbLink asChild><Link to={base.replace(/\/inventory$/, '')}>Bare Metal</Link></BreadcrumbLink></BreadcrumbItem>
      <BreadcrumbSeparator />
      <BreadcrumbItem>{directory || category ? <BreadcrumbLink asChild><Link to={base}>Inventory</Link></BreadcrumbLink> : <BreadcrumbPage>Inventory</BreadcrumbPage>}</BreadcrumbItem>
      {group && <><BreadcrumbSeparator /><BreadcrumbItem><BreadcrumbLink asChild><Link to={`${base}?kind=${group.kinds[0]}`}>{group.title}</Link></BreadcrumbLink></BreadcrumbItem></>}
      {category && <><BreadcrumbSeparator /><BreadcrumbItem>{directory ? <BreadcrumbLink asChild><Link to={`${base}${directory}`}>{category.title}</Link></BreadcrumbLink> : <BreadcrumbPage>{category.title}</BreadcrumbPage>}</BreadcrumbItem></>}
      {directory && <><BreadcrumbSeparator /><BreadcrumbItem><BreadcrumbPage>{title}</BreadcrumbPage></BreadcrumbItem></>}
    </BreadcrumbList></Breadcrumb>
    <h1 className="sr-only" aria-label={title} tabIndex={-1} ref={heading} />
    <PageTitle title={title} description={description} actions={actions} actionsClassName="inventory-title-actions" className="inventory-page-title" />
  </div>;
}
