import { Button } from '@datum-cloud/datum-ui/button';
import { Input } from '@datum-cloud/datum-ui/input';
import { Label } from '@datum-cloud/datum-ui/label';
import { RefreshCw, Search } from 'lucide-react';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router';
import { FieldSelect } from './field-select';
import { sections } from './inventory-navigation';
import './physical-inventory.css';

/** Search applies this long after the last keystroke; Enter applies it at once. */
export const SEARCH_DELAY_MS = 200;

type Section = typeof sections[number];

/** The directory's only heading. The tabs show where the reader is, so it is not visible. */
export function InventoryDirectoryHeading({ section }: { section: Section }) {
  return <h1 className="sr-only">{section.title}</h1>;
}

export function InventoryPendingShell({ children }: { children: ReactNode }) {
  const [params] = useSearchParams();
  const section = sections.find(item => item.kind === params.get('kind')) ?? sections[0];
  return <section className="fleet-inventory" aria-label="Physical inventory"><div className="inventory-body"><InventoryDirectoryHeading section={section} /><InventoryToolbar section={section} />{children}</div></section>;
}

/** The disabled Create button that holds the slot until management access resolves. */
function CreatePlaceholder({ section }: { section: Section }) {
  if (!['Facility', 'Rack', 'Device', 'Cable', 'DeviceClaim'].includes(section.kind)) return null;
  return <><div className="inventory-management-controls"><Button type="secondary" theme="outline" size="small" htmlType="button" disabled>Create {section.kind === 'DeviceClaim' ? 'installation plan' : section.kind.toLowerCase()}</Button></div><p role="status" className="inventory-management-status" /></>;
}

/** The search box applies as the reader types; the URL keeps the applied value. */
function SearchField({ section, search, onSearch }: { section: Section; search: string; onSearch?: (value: string) => void }) {
  const [draft, setDraft] = useState(search);
  const applied = useRef(search);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => {
    if (search === applied.current) return;
    applied.current = search;
    setDraft(search);
  }, [search]);
  useEffect(() => () => clearTimeout(timer.current), []);
  const apply = (value: string) => { clearTimeout(timer.current); applied.current = value; onSearch?.(value); };
  return <form className="inventory-search-form" role="search" onSubmit={event => { event.preventDefault(); apply(draft); }}>
    <Label className="inventory-search"><Search size={18} aria-hidden="true" /><Input aria-label={`Search ${section.title.toLowerCase()}`} placeholder={`Search ${section.title.toLowerCase()}…`} value={draft} disabled={!onSearch} type="search"
      onChange={event => { const value = event.target.value; setDraft(value); clearTimeout(timer.current); timer.current = setTimeout(() => apply(value), SEARCH_DELAY_MS); }} /></Label>
  </form>;
}

/** One row above the table: search, filters, Clear filters when one is set, Refresh, then Create. */
export function InventoryToolbar({ section, search = '', onSearch, location = '', onLocation, facilities = [], model = '', onModel, models = [], onClear, onRetry, management }: {
  section: Section; search?: string; onSearch?: (value: string) => void;
  location?: string; onLocation?: (value: string) => void; facilities?: { value: string; label: string }[];
  model?: string; onModel?: (value: string) => void; models?: string[];
  onClear?: () => void; onRetry?: () => void; management?: ReactNode;
}) {
  const hasLocation = ['Device', 'DeviceClaim', 'Rack', 'PowerFeed'].includes(section.kind);
  const hasModel = ['Device', 'Cable'].includes(section.kind);
  return <div className="inventory-toolbar">
    <SearchField section={section} search={search} onSearch={onSearch} />
    {hasLocation && <FieldSelect aria-label="Filter by facility" value={location} disabled={!onLocation} onValueChange={value => onLocation?.(value)} none="All facilities" options={facilities} />}
    {hasModel && <FieldSelect aria-label={section.kind === 'Cable' ? 'Filter by medium' : 'Filter by model'} value={model} disabled={!onModel} onValueChange={value => onModel?.(value)} none={section.kind === 'Cable' ? 'All media' : 'All models'} options={models.map(value => ({ value, label: value }))} />}
    {(location || model) && onClear && <Button type="secondary" theme="outline" size="small" htmlType="button" onClick={onClear}>Clear filters</Button>}
    <div className="inventory-toolbar-actions">
      <Button type="secondary" theme="outline" size="small" htmlType="button" disabled={!onRetry} onClick={onRetry} aria-label="Refresh inventory" title="Refresh inventory"><RefreshCw size={16} aria-hidden="true" /></Button>
      {management ?? <CreatePlaceholder section={section} />}
    </div>
  </div>;
}
