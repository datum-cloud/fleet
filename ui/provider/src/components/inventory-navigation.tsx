import { Link, useLocation, useSearchParams } from 'react-router';
import { Tabs, TabsLinkTrigger, TabsList } from '@datum-cloud/datum-ui/tabs';
import { Building2, Cable, ClipboardList, HardDrive, Server, Zap } from 'lucide-react';
import type { Inventory } from '../inventory';
import { inventoryDirectoryPath, inventoryReturnSearch } from './inventory-page-title';
import { inventoryGroup, inventoryGroups } from './inventory-groups';
import type { MouseEvent } from 'react';
import './physical-inventory.css';

export const sections = [
  {
    kind: 'Device',
    title: 'Devices',
    icon: HardDrive,
    description: 'Physical hardware, identity, and recorded connections.',
  },
  {
    kind: 'Site',
    title: 'Operational sites',
    icon: Building2,
    description: 'Internal operational identity and recorded premises membership. Not placement eligibility or authority.',
  },
  {
    kind: 'Facility',
    title: 'Facilities',
    icon: Building2,
    description: 'Premises and the racks and equipment recorded within them.',
  },
  {
    kind: 'Rack',
    title: 'Racks',
    icon: Server,
    description: 'Rack definitions and reported equipment placement.',
  },
  {
    kind: 'DeviceClaim',
    title: 'Claims & registrations',
    icon: ClipboardList,
    description: 'Registration and intent history, kept separate from physical hardware. Installation lifecycle alone does not distinguish prospective from existing equipment.',
  },
  {
    kind: 'Cable',
    title: 'Cables',
    icon: Cable,
    description: 'Recorded cable lanes and their physical terminations.',
  },
  {
    kind: 'PowerFeed',
    title: 'Power feeds',
    icon: Zap,
    description: 'Supply ratings, terminations, and upstream sources.',
  },
  { kind: 'Manufacturer', title: 'Manufacturers', icon: Building2, description: 'Shared canonical names and descriptive aliases. Aliases never merge identities.' },
  { kind: 'DeviceType', title: 'Device types', icon: Server, description: 'Reference-only model shapes and component templates, not installed equipment or occupancy.' },
] as const;

export function InventoryNavigation({ kind, ..._props }: { inventory?: Inventory; kind?: string }) {
  const [params] = useSearchParams();
  const { pathname, search } = useLocation();
  const active = sections.find(item => item.kind === (kind ?? params.get('kind'))) ?? sections[0];
  const group = inventoryGroup(active.kind);
  const href = (next: string) => `${inventoryDirectoryPath(pathname)}${inventoryReturnSearch(next === active.kind ? search : '', next)}`;
  const subcategories = sections.filter(item => group.kinds.some(kind => kind === item.kind));
  const stayOnCurrentPage = (event: MouseEvent<HTMLElement>, target: string) => {
    if (!event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && target === `${pathname}${search}`) event.preventDefault();
  };
  const trigger = (value: string, title: string, selected: boolean) => <TabsLinkTrigger
    key={value} value={value} href={href(value)} linkComponent={Link}
    onClick={event => stayOnCurrentPage(event, href(value))}
    aria-current={selected ? 'page' : undefined} aria-controls={undefined}
    onKeyDown={event => { if (event.key === ' ') { event.preventDefault(); event.currentTarget.click(); } }}>
    {title}
  </TabsLinkTrigger>;
  return <nav className="inventory-sections" aria-label="Inventory categories">
    <Tabs value={group.title} activationMode="manual" className="inventory-tab-row">
      <TabsList variant="line" aria-label="Inventory groups">
        {inventoryGroups.map(item => <TabsLinkTrigger key={item.title} value={item.title}
          href={href(item === group ? active.kind : item.kinds[0])} linkComponent={Link}
          onClick={event => stayOnCurrentPage(event, href(item === group ? active.kind : item.kinds[0]))}
          aria-current={item === group ? 'page' : undefined} aria-controls={undefined}
          onKeyDown={event => { if (event.key === ' ') { event.preventDefault(); event.currentTarget.click(); } }}>
          {item.title}
        </TabsLinkTrigger>)}
      </TabsList>
    </Tabs>
    <div className="inventory-subcategories">
      {subcategories.length > 1 && <Tabs value={active.kind} activationMode="manual" className="inventory-tab-row">
        <TabsList variant="line" aria-label={`${group.title} categories`}>
          {subcategories.map(item => trigger(item.kind, item.title, item.kind === active.kind))}
        </TabsList>
      </Tabs>}
    </div>
    <p className="inventory-group-description">{group.description}</p>
  </nav>;
}
