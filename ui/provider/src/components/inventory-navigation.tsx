import { Link, useLocation, useSearchParams } from 'react-router';
import { Button } from '@datum-cloud/datum-ui/button';
import { ButtonGroup } from '@datum-cloud/datum-ui/button-group';
import { Popover, PopoverContent, PopoverTrigger } from '@datum-cloud/datum-ui/popover';
import { Box, Building2, Cable, Check, ChevronDown, ClipboardList, Factory, HardDrive, MapPin, Server, Zap } from 'lucide-react';
import { inventoryDirectoryPath, inventoryReturnSearch } from './inventory-page-title';
import { CATALOG_KINDS, inventoryGroups, isCatalogKind } from './inventory-groups';
import { createContext, useContext, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type MouseEvent, type ReactNode, type RefObject } from 'react';
import './physical-inventory.css';

// `short` is the tab label at phone width, where it differs.
export const sections = [
  { kind: 'Device', title: 'Devices', icon: HardDrive },
  { kind: 'Site', title: 'Operational sites', short: 'Sites', icon: MapPin },
  { kind: 'Facility', title: 'Facilities', icon: Building2 },
  { kind: 'Rack', title: 'Racks', icon: Server },
  { kind: 'DeviceClaim', title: 'Installation plans', icon: ClipboardList },
  { kind: 'Cable', title: 'Cables', icon: Cable },
  { kind: 'PowerFeed', title: 'Power feeds', icon: Zap },
  { kind: 'Manufacturer', title: 'Manufacturers', icon: Factory },
  { kind: 'DeviceType', title: 'Device types', icon: Box },
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

function countWords(count: { total?: number; shown?: number }) {
  const filtered = count.total !== undefined && count.shown !== undefined && count.shown !== count.total;
  const text = count.total === undefined ? '?' : filtered ? `${count.shown}/${count.total}` : String(count.total);
  const words = count.total === undefined ? 'Count unknown: this collection is unavailable' : filtered ? `${count.shown} of ${count.total} records` : `${count.total} ${count.total === 1 ? 'record' : 'records'}`;
  return { text, words };
}

/** The visible count beside a category; its words are the category's description (CountWords), not part of its name. */
function CountMark({ count }: { count?: { total?: number; shown?: number } }) {
  if (!count) return null;
  const { text, words } = countWords(count);
  return <span className="inventory-tab-count" aria-hidden="true" title={count.total === undefined ? words : undefined}>{text}</span>;
}

function CountWords({ id, count }: { id: string; count?: { total?: number; shown?: number } }) {
  if (!count) return null;
  return <span id={id} className="inventory-visually-hidden">{countWords(count).words}</span>;
}

/** Publishes the navigation's width to the list frame so the toolbar row can reserve it. */
function useFrameWidth(nav: RefObject<HTMLElement | null>) {
  useLayoutEffect(() => {
    const element = nav.current;
    const frame = element?.closest<HTMLElement>('.inventory-list-frame');
    if (!element || !frame) return;
    const publish = () => frame.style.setProperty('--inventory-switcher-w', `${element.offsetWidth}px`);
    publish();
    if (typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(publish);
    observer.observe(element);
    return () => observer.disconnect();
  });
}

/**
 * Inventory and Catalog navigation in staff-portal's list style.
 *
 * Inventory: one category switcher (like staff-portal's SectionSwitcher, a
 * datum-ui Popover of plain links grouped by Equipment, Locations and
 * Installation plans), each category with its record count. Catalog: its two
 * categories as a pill toggle (like staff-portal's ViewTabs). The nav renders
 * outside the session boundary; counts appear only once PhysicalInventory has
 * reported them from inside it. On list pages the page frame
 * (`.inventory-list-frame`) lays the nav over the left end of the toolbar row,
 * which reserves `--inventory-switcher-w` for it.
 */
export function InventoryNavigation({ kind, counts: given, scope = 'inventory' }: { kind?: string; counts?: InventoryCounts; scope?: 'inventory' | 'catalog' }) {
  const reported = useContext(InventoryCountsContext).counts;
  const counts = given ?? reported;
  return scope === 'catalog' ? <CatalogNavigation counts={counts} /> : <CategorySwitcher kind={kind} counts={counts} />;
}

function useCategoryHref(activeKind: string) {
  const { pathname, search } = useLocation();
  const href = (next: string) => `${inventoryDirectoryPath(pathname, next)}${inventoryReturnSearch(next === activeKind ? search : '', next)}`;
  const current = `${pathname}${search}`;
  return { href, current };
}

function CatalogNavigation({ counts }: { counts?: InventoryCounts }) {
  const [params] = useSearchParams();
  const nav = useRef<HTMLElement>(null);
  useFrameWidth(nav);
  const countID = useId();
  const active = isCatalogKind(params.get('kind')) ? params.get('kind')! : CATALOG_KINDS[0];
  const { href, current } = useCategoryHref(active);
  return <nav ref={nav} className="inventory-sections inventory-catalog-toggle" aria-label="Catalog categories">
    {CATALOG_KINDS.map(kind => <CountWords key={`words-${kind}`} id={`${countID}-${kind}`} count={counts?.[kind]} />)}
    <ButtonGroup>{CATALOG_KINDS.map(kind => {
      const section = sections.find(item => item.kind === kind)!;
      const Icon = section.icon;
      const target = href(kind);
      return <Button key={kind} asChild type={kind === active ? 'primary' : 'secondary'} theme={kind === active ? 'light' : 'outline'} size="small">
        <Link to={target} aria-current={kind === active ? 'page' : undefined} aria-describedby={counts?.[kind] ? `${countID}-${kind}` : undefined}
          onClick={event => { if (target === current) event.preventDefault(); }}>
          <Icon size={16} aria-hidden="true" />{section.title}<CountMark count={counts?.[kind]} />
        </Link>
      </Button>;
    })}</ButtonGroup>
  </nav>;
}

function CategorySwitcher({ kind, counts }: { kind?: string; counts?: InventoryCounts }) {
  const [params] = useSearchParams();
  const { pathname, search } = useLocation();
  const requested = kind ?? params.get('kind');
  const active = sections.find(item => item.kind === requested && !isCatalogKind(item.kind)) ?? sections[0];
  const { href, current } = useCategoryHref(active.kind);
  const [open, setOpen] = useState(false);
  const menu = useRef<HTMLDivElement>(null);
  const menuLabel = useId();
  const countID = useId();
  const nav = useRef<HTMLElement>(null);
  useEffect(() => { setOpen(false); }, [pathname, search]);
  useFrameWidth(nav);
  const onMenuKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    const items = [...(menu.current?.querySelectorAll<HTMLAnchorElement>('a') ?? [])];
    const index = items.indexOf(event.target as HTMLAnchorElement);
    if (index < 0) return;
    event.preventDefault();
    items[(index + (event.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length]?.focus();
  };
  const select = (event: MouseEvent<HTMLAnchorElement>, target: string) => {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    if (target === current) event.preventDefault();
    setOpen(false);
  };
  const ActiveIcon = active.icon;
  return <nav ref={nav} className="inventory-sections" aria-label="Inventory categories">
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button type="secondary" theme="borderless" size="small" htmlType="button" className="inventory-switcher" aria-describedby={counts?.[active.kind] ? `${countID}-trigger` : undefined} onKeyDown={event => { if (!open && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) { event.preventDefault(); setOpen(true); } }}>
          <ActiveIcon size={16} aria-hidden="true" />
          <span><span className="sr-only">Inventory category: </span>{active.title}</span>
          <CountMark count={counts?.[active.kind]} />
          <ChevronDown size={14} aria-hidden="true" className="inventory-switcher-chevron" />
        </Button>
      </PopoverTrigger>
      <PopoverContent ref={menu} align="start" className="inventory-switcher-menu" aria-labelledby={menuLabel} onKeyDown={onMenuKeyDown}
        onOpenAutoFocus={event => { event.preventDefault(); menu.current?.querySelector<HTMLAnchorElement>('a[aria-current="page"]')?.focus(); }}>
        <span id={menuLabel} className="sr-only">Inventory categories</span>
        {inventoryGroups.map(group => <div key={group.title} className="inventory-switcher-group">
          <p className="inventory-switcher-group-label" id={`${menuLabel}-${group.kinds[0]}`}>{group.title}</p>
          <ul aria-labelledby={`${menuLabel}-${group.kinds[0]}`}>
            {group.kinds.map(item => {
              const section = sections.find(candidate => candidate.kind === item)!;
              const Icon = section.icon;
              const target = href(section.kind);
              const selected = section.kind === active.kind;
              return <li key={section.kind}><Link to={target} aria-current={selected ? 'page' : undefined} aria-describedby={counts?.[section.kind] ? `${countID}-${section.kind}` : undefined} onClick={event => select(event, target)}>
                <Icon size={16} aria-hidden="true" /><span>{section.title}</span><CountMark count={counts?.[section.kind]} />{selected && <Check size={16} aria-hidden="true" className="inventory-switcher-check" />}
              </Link><CountWords id={`${countID}-${section.kind}`} count={counts?.[section.kind]} /></li>;
            })}
          </ul>
        </div>)}
      </PopoverContent>
    </Popover>
    <CountWords id={`${countID}-trigger`} count={counts?.[active.kind]} />
  </nav>;
}
