import { Button } from '@datum-cloud/datum-ui/button';
import { Input } from '@datum-cloud/datum-ui/input';
import { Label } from '@datum-cloud/datum-ui/label';
import { Search } from 'lucide-react';
import type { ReactNode } from 'react';
import { useSearchParams } from 'react-router';
import { sections } from './inventory-navigation';
import './physical-inventory.css';
import { InventoryPageTitle } from './inventory-page-title';

export function InventoryDirectoryHeading({ section, count, management, onRetry }: { section: typeof sections[number]; count?: string; management?: ReactNode; onRetry?: () => void }) {
  const creatable = ['Facility', 'Rack', 'Device', 'Cable', 'DeviceClaim'].includes(section.kind);
  return <div className="inventory-topline inventory-directory-title">
    <InventoryPageTitle title={section.title} category={section} description={section.description} actions={<>
      <span className="inventory-record-count inventory-muted">{count}</span>
      <Button type="secondary" theme="outline" size="small" htmlType="button" disabled={!onRetry} onClick={onRetry}>Refresh inventory</Button>
    {management ?? <><div className="inventory-management-controls">{creatable && <Button type="secondary" theme="outline" size="small" htmlType="button" disabled>Create {section.kind === 'DeviceClaim' ? 'installation plan' : section.kind.toLowerCase()}</Button>}</div><p role="status" className="inventory-management-status" /></>}
    </>} />
  </div>;
}

export function InventoryPendingShell({ children }: { children: ReactNode }) {
  const [params] = useSearchParams();
  const section = sections.find(item => item.kind === params.get('kind')) ?? sections[0];
  return <section className="fleet-inventory" aria-label="Physical inventory"><div className="inventory-body"><InventoryDirectoryHeading section={section} /><InventoryToolbar section={section} />{children}</div></section>;
}

export function InventoryToolbar({ section, search = '', onSearch, onApplySearch, location = '', onLocation, facilities = [], model = '', onModel, models = [], onClear }: {
  section: typeof sections[number]; search?: string; onSearch?: (value: string) => void;
  location?: string; onLocation?: (value: string) => void; facilities?: { value: string; label: string }[];
  model?: string; onModel?: (value: string) => void; models?: string[];
  onClear?: () => void;
  onApplySearch?: () => void;
}) {
  const hasLocation = ['Device', 'DeviceClaim', 'Rack', 'PowerFeed'].includes(section.kind);
  const hasModel = ['Device', 'Cable'].includes(section.kind);
  return <div className="inventory-toolbar">
    <form className="inventory-search-form" onSubmit={event => { event.preventDefault(); onApplySearch?.(); }}><Label className="inventory-search"><Search size={18} aria-hidden="true" /><Input aria-label={`Search ${section.title.toLowerCase()}`} placeholder={`Search ${section.title.toLowerCase()} by name, identity, or location…`} value={search} disabled={!onSearch} onChange={event => onSearch?.(event.target.value)} type="search" /></Label><Button type="secondary" theme="outline" size="small" htmlType="submit" disabled={!onApplySearch}>Apply search</Button></form>
    {hasLocation && <select aria-label="Filter by facility" value={location} disabled={!onLocation} onChange={event => onLocation?.(event.target.value)}><option value="">All facilities</option>{facilities.map(item => <option key={item.value} value={item.value}>{item.label}</option>)}</select>}
    {hasModel && <select aria-label={section.kind === 'Cable' ? 'Filter by medium' : 'Filter by model'} value={model} disabled={!onModel} onChange={event => onModel?.(event.target.value)}><option value="">{section.kind === 'Cable' ? 'All media' : 'All models'}</option>{models.map(value => <option key={value}>{value}</option>)}</select>}
    {(hasLocation || hasModel) && <Button type="secondary" theme="outline" size="small" htmlType="button" disabled={!location && !model} onClick={onClear}>Clear filters</Button>}
  </div>;
}
