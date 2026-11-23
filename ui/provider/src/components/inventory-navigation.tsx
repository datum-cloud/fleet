import { Link, useLocation, useSearchParams } from 'react-router';
import { Tabs, TabsLinkTrigger, TabsList } from '@datum-cloud/datum-ui/tabs';
import { Building2, Cable, ClipboardList, HardDrive, Server, Zap } from 'lucide-react';
import { inventoryDirectoryPath, inventoryReturnSearch } from './inventory-page-title';
import { inventoryGroup, inventoryGroups } from './inventory-groups';
import { createContext, useContext, useEffect, useId, useMemo, useState, type MouseEvent, type ReactNode } from 'react';
import './physical-inventory.css';

// `short` is the tab label at phone width, where it differs.
export const sections = [
  { kind: 'Device', title: 'Devices', icon: HardDrive },
  { kind: 'Site', title: 'Operational sites', short: 'Sites', icon: Building2 },
  { kind: 'Facility', title: 'Facilities', icon: Building2 },
  { kind: 'Rack', title: 'Racks', icon: Server },
  { kind: 'DeviceClaim', title: 'Installation plans', icon: ClipboardList },
  { kind: 'Cable', title: 'Cables', icon: Cable },
  { kind: 'PowerFeed', title: 'Power feeds', icon: Zap },
  { kind: 'Manufacturer', title: 'Manufacturers', icon: Building2 },
  { kind: 'DeviceType', title: 'Device types', icon: Server },
] as const;

/**
 * Directory counts for the kind tabs: `total` undefined means the collection
 * is unreadable ("?"), `shown` is the filtered count on the active directory.
 * Only PhysicalInventory reports them, after the session boundary has
 * resolved, so the public shell never renders a private count.
 */
export type InventoryCounts = Record<string, { total?: number; shown?: number }>;

const InventoryCountsContext = createContext<{ counts?: InventoryCounts; report: (counts?: InventoryCounts) => void }>({ report: () => {} });

export function InventoryCountsProvider({ children }: { children: ReactNode }) {
  const [counts, setCounts] = useState<InventoryCounts>();
  const value = useMemo(() => ({ counts, report: setCounts }), [counts]);
  return <InventoryCountsContext.Provider value={value}>{children}</InventoryCountsContext.Provider>;
}

/** Publishes counts to the navigation for as long as the caller is mounted. */
export function useReportInventoryCounts(counts: InventoryCounts) {
  const { report } = useContext(InventoryCountsContext);
  const key = JSON.stringify(counts);
  useEffect(() => {
    report(JSON.parse(key) as InventoryCounts);
    return () => report(undefined);
  }, [key, report]);
}

/** The count beside a kind tab. The tab's name stays its title; the count is its description. */
function TabCount({ id, count }: { id: string; count?: { total?: number; shown?: number } }) {
  if (!count) return null;
  const filtered = count.total !== undefined && count.shown !== undefined && count.shown !== count.total;
  const text = count.total === undefined ? '?' : filtered ? `${count.shown}/${count.total}` : String(count.total);
  const words = count.total === undefined ? 'Count unknown: this collection is unavailable' : filtered ? `${count.shown} of ${count.total} records` : `${count.total} ${count.total === 1 ? 'record' : 'records'}`;
  return <><span className="inventory-tab-count" aria-hidden="true" title={count.total === undefined ? words : undefined}>{text}</span><span id={id} className="inventory-visually-hidden">{words}</span></>;
}

function TabLabel({ title, short }: { title: string; short?: string }) {
  return short && short !== title ? <><span className="inventory-tab-long">{title}</span><span className="inventory-tab-short" aria-hidden="true">{short}</span></> : <>{title}</>;
}

export function InventoryNavigation({ kind, counts: given }: { kind?: string; counts?: InventoryCounts }) {
  const [params] = useSearchParams();
  const reported = useContext(InventoryCountsContext).counts;
  const counts = given ?? reported;
  const { pathname, search } = useLocation();
  const active = sections.find(item => item.kind === (kind ?? params.get('kind'))) ?? sections[0];
  const group = inventoryGroup(active.kind);
  const href = (next: string) => `${inventoryDirectoryPath(pathname)}${inventoryReturnSearch(next === active.kind ? search : '', next)}`;
  const subcategories = sections.filter(item => group.kinds.some(kind => kind === item.kind));
  const stayOnCurrentPage = (event: MouseEvent<HTMLElement>, target: string) => {
    if (!event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && target === `${pathname}${search}`) event.preventDefault();
  };
  const countID = useId();
  const trigger = (value: string, title: string, short: string | undefined, selected: boolean) => <TabsLinkTrigger
    key={value} value={value} href={href(value)} linkComponent={Link}
    aria-label={title} aria-describedby={counts?.[value] ? `${countID}-${value}` : undefined}
    onClick={event => stayOnCurrentPage(event, href(value))}
    aria-current={selected ? 'page' : undefined} aria-controls={undefined}
    onKeyDown={event => { if (event.key === ' ') { event.preventDefault(); event.currentTarget.click(); } }}>
    <TabLabel title={title} short={short} /><TabCount id={`${countID}-${value}`} count={counts?.[value]} />
  </TabsLinkTrigger>;
  return <nav className="inventory-sections" aria-label="Inventory categories">
    <Tabs value={group.title} activationMode="manual" className="inventory-tab-row">
      <TabsList variant="line" aria-label="Inventory groups">
        {inventoryGroups.map(item => <TabsLinkTrigger key={item.title} value={item.title}
          href={href(item === group ? active.kind : item.kinds[0])} linkComponent={Link}
          aria-label={item.title}
          onClick={event => stayOnCurrentPage(event, href(item === group ? active.kind : item.kinds[0]))}
          aria-current={item === group ? 'page' : undefined} aria-controls={undefined}
          onKeyDown={event => { if (event.key === ' ') { event.preventDefault(); event.currentTarget.click(); } }}>
          <TabLabel title={item.title} short={item.short} />
        </TabsLinkTrigger>)}
      </TabsList>
    </Tabs>
    {subcategories.length > 1 && <Tabs value={active.kind} activationMode="manual" className="inventory-tab-row">
      <TabsList variant="line" aria-label={`${group.title} categories`}>
        {subcategories.map(item => trigger(item.kind, item.title, 'short' in item ? item.short : undefined, item.kind === active.kind))}
      </TabsList>
    </Tabs>}
  </nav>;
}
