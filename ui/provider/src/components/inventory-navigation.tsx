import { Link, useSearchParams } from 'react-router';
import { LinkButton } from '@datum-cloud/datum-ui/button';
import { Building2, Cable, ClipboardList, HardDrive, Server, Zap } from 'lucide-react';
import type { Inventory } from '../inventory';
import { inventoryHref } from './inventory-location';

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
] as const;

export function InventoryNavigation({ inventory }: { inventory?: Inventory }) {
  const [params] = useSearchParams();
  const active = sections.find(item => item.kind === params.get('kind')) ?? sections.find(item => !inventory?.issues?.[item.kind]) ?? sections[0];
  return <nav className="inventory-sections" aria-label="Inventory categories">
    {sections.filter(item => inventory?.issues?.[item.kind]?.status !== 403).map(item => <LinkButton
      key={item.kind} as={Link} href={inventoryHref(item.kind)} type="secondary"
      theme={active.kind === item.kind ? 'light' : 'borderless'} size="small"
      aria-current={active.kind === item.kind ? 'page' : undefined}>
      <item.icon size={18} aria-hidden="true" /><span>{item.title}</span>
    </LinkButton>)}
  </nav>;
}
