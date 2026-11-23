import { Button } from '@datum-cloud/datum-ui/button';
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@datum-cloud/datum-ui/table';
import { Title } from '@datum-cloud/datum-ui/typography';
import { useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { slotKey, type Inventory, type InventorySlot } from '../inventory';
import type { PhysicalRecord, RackFace as RackFaceName } from '../../../shared/fleet-api';
import { depthLabel } from '../lib/device-depth';
import { faceOccupancy, rackOccupants, type RackOccupant } from '../lib/rack-faces';
import { equipmentGeometry, equipmentPlacements, reportedPlacementLabel, type EquipmentPlacement } from '../lib/device-placement';
import { RackSlotDevice } from './rack-slot-device';

/** How the elevation links a device-in-slot: to its record, or to a selection on the rack page. */
export interface RackSelection { href: (slot: EquipmentPlacement) => string; selectedKey?: string }

export function inventoryHref(kind: string, namespace?: string, name?: string) {
  const params = new URLSearchParams({ kind });
  if (name !== undefined) { params.set('record', name); params.set('namespace', namespace ?? ''); }
  return `?${params}`;
}

export function rackGeometry(slot: InventorySlot, rack: PhysicalRecord) {
  return equipmentGeometry({ ...slot, kind: 'DeviceClaim', record: slot.claim, placement: slot.claim.spec.placement, basis: 'Claim-derived fallback' }, rack);
}

function Equipment({ slots, devicesDenied, inventory }: { slots: EquipmentPlacement[]; devicesDenied: boolean; inventory: Inventory }) {
  return <div className="inventory-scroll"><Table aria-label="Recorded equipment"><TableHeader><TableRow><TableHead>Equipment</TableHead><TableHead>Claim / registration history</TableHead><TableHead>Location / Reported placement</TableHead></TableRow></TableHeader><TableBody>
    {slots.map(slot => <TableRow key={slot.key}><TableCell>{devicesDenied ? 'Device details unavailable' : slot.devices.length ? slot.devices.map(device => <div key={device.metadata.name}><Link to={inventoryHref('Device', device.metadata.namespace, device.metadata.name)}>{device.spec.displayName || device.metadata.name}</Link></div>) : 'No device registration recorded'}{slot.devices.length > 1 && <small>Multiple registrations · active device unknown</small>}</TableCell><TableCell>{slot.claim ? <Link to={inventoryHref('DeviceClaim', slot.claim.metadata.namespace, slot.claim.metadata.name)}>{slot.claim.spec.displayName || slot.claim.metadata.name}</Link> : 'Claim details unavailable'}</TableCell><TableCell>{reportedPlacementLabel(slot, inventory)}</TableCell></TableRow>)}
  </TableBody></Table></div>;
}

/**
 * One rack face. Every unit is drawn: one grid row per unit (row = height −
 * unit + 1), so a placement's row never moves. Entries are in visual order,
 * top to bottom, so arrow keys follow the drawing.
 */
function RackFace({ occupants, height, side, selection, inventory, unit }: { occupants: RackOccupant[]; height: number; side: RackFaceName; selection?: RackSelection; inventory: Inventory; unit: number }) {
  const plans = faceOccupancy(occupants, side)
    .sort((first, second) => first.position - second.position || first.entry.key.localeCompare(second.entry.key));
  const laneEnds: number[] = [];
  const placed = plans.map(plan => {
    let lane = laneEnds.findIndex(end => end < plan.position);
    if (lane === -1) lane = laneEnds.length;
    laneEnds[lane] = plan.position + plan.height - 1;
    return { ...plan, lane };
  });
  const entries: ReactNode[] = [];
  for (let value = height; value >= 1; value--) {
    const recorded = plans.some(plan => value >= plan.position && value < plan.position + plan.height);
    entries.push(<li key={value} className="inventory-rack-unit" style={{ gridRow: height - value + 1, gridColumn: '1 / -1' }}>
      <span>U{value}</span>{!recorded && <span className="inventory-muted" aria-label="Occupancy unverified">—</span>}
    </li>);
    for (const plan of placed) if (plan.position + plan.height - 1 === value) entries.push(planEntry(plan));
  }

  function planEntry(plan: (typeof placed)[number]) {
        const top = plan.position + plan.height - 1;
        const overlap = plan.conflicts.length > 0;
        const record = plan.entry.record;
        const recordName = record.spec.displayName || record.metadata.name;
        // The far face of a full-depth (or unknown-depth) device shows its
        // chassis rear, whichever rack face it is mounted on.
        const name = plan.far ? `Rear of ${recordName}` : recordName;
        const range = plan.height === 1 ? `U${plan.position}` : `U${plan.position}–U${top}`;
        const label = [name, range, `${plan.height}U`, plan.entry.basis, plan.far ? `mounted ${plan.mounted}` : undefined, depthLabel(plan.depth), overlap ? 'records disagree: overlapping placements on this face' : undefined].filter(Boolean).join(' · ');
        const selected = selection?.selectedKey === plan.entry.key;
        return <li key={plan.entry.key} className={`inventory-rack-plan${plan.far ? ' inventory-rack-plan-far' : ''}`} data-far={plan.far ? '' : undefined} data-depth={plan.depth.source === 'unknown' ? 'unknown' : plan.depth.full ? 'full' : 'half'} data-selected={selected ? '' : undefined} style={{ gridRow: `${height - top + 1} / span ${plan.height}`, gridColumn: plan.lane + 2 }}>
          <RackSlotDevice slot={plan.entry} name={recordName} label={label} side={side} overlap={overlap} far={plan.far} to={selection ? selection.href(plan.entry) : inventoryHref(plan.entry.kind, record.metadata.namespace, record.metadata.name)} selected={selected} inventory={inventory} />
        </li>;
  }

  return <div><h4>{side === 'front' ? 'Front' : 'Rear'}</h4>
    <ol className="inventory-rack-units" aria-label={`${side} recorded placements`} data-face={side} style={{ gridTemplateRows: `repeat(${height}, ${unit}px)`, gridTemplateColumns: `28px repeat(${Math.max(1, laneEnds.length)}, minmax(0, 1fr))` }}>
      {entries}
    </ol>
  </div>;
}

/** The rack's recorded placements within this rack, the elevation's input. */
export function rackPlacements(rack: PhysicalRecord, inventory: Inventory) {
  return equipmentPlacements(inventory).filter(slot => slot.record.metadata.namespace === rack.metadata.namespace && slot.placement.rackRef?.name === rack.metadata.name);
}

/**
 * The rack elevation. The rack page wraps it: selection, tabs and issue
 * banners live in rack-detail.tsx. `toolbar` sits beside the face switch,
 * where a later 2D/3D view toggle belongs.
 */
/** Pixels per rack unit: a device's name fits one unit. */
export const UNIT_PX = 18;

export function RackOverview({ rack, inventory, selection, toolbar }: { rack: PhysicalRecord; inventory: Inventory; selection?: RackSelection; toolbar?: ReactNode }) {
  const [face, setFace] = useState<'both' | 'front' | 'rear'>('both');
  const slots = rackPlacements(rack, inventory);
  const positioned = slots.filter(slot => equipmentGeometry(slot, rack));
  const occupants = rackOccupants(positioned, rack, inventory);
  const denied = !!inventory.issues?.DeviceClaim && !slots.length;
  const height = rack.spec.heightUnits;
  return <section className="inventory-rack-layout" aria-label="Rack layout">
    <aside className="inventory-elevation">
      <div className="inventory-list-heading"><Title as="h3" level={5}>Rack elevation</Title><span>{height ? `${height}U` : 'Height unknown'}</span></div>
      <div className="inventory-elevation-toolbar"><div className="inventory-face-switch" role="group" aria-label="Rack faces">{(['both', 'front', 'rear'] as const).map(value => <Button type="secondary" theme={face === value ? 'light' : 'outline'} size="small" key={value} htmlType="button" aria-pressed={face === value} onClick={() => setFace(value)}>{value === 'both' ? 'Front + rear' : value === 'front' ? 'Front' : 'Rear'}</Button>)}</div>{toolbar}</div>
      {denied ? <p>Installation details unavailable. Rack positions cannot be shown.</p> : !height ? <p>Rack height is not recorded. Equipment remains listed without invented slots.</p> : <div className="inventory-rack-faces">{(['front', 'rear'] as const).filter(value => face === 'both' || face === value).map(side => <RackFace key={side} occupants={occupants} height={height} side={side} selection={selection} inventory={inventory} unit={UNIT_PX} />)}</div>}
    </aside>
  </section>;
}

const facilityRacks = (facility: PhysicalRecord, inventory: Inventory) => inventory.physical.filter(record => record.kind === 'Rack' && record.metadata.namespace === facility.metadata.namespace && record.spec.facilityRef?.name === facility.metadata.name);
const facilityOnly = (facility: PhysicalRecord, inventory: Inventory) => equipmentPlacements(inventory).filter(slot => slot.record.metadata.namespace === facility.metadata.namespace && !slot.placement.rackRef && slot.placement.facilityRef?.name === facility.metadata.name);

/** A Facility's primary view: its racks. Free space is stated once, as unknown. */
export function FacilityRacks({ facility, inventory }: { facility: PhysicalRecord; inventory: Inventory }) {
  const racks = facilityRacks(facility, inventory);
  const equipment = equipmentPlacements(inventory);
  const incomplete = !!inventory.issues?.DeviceClaim || !!inventory.issues?.Device;
  return <section className="inventory-equipment-sections" aria-label="Facility contents">
    <Title as="h3" level={5}>Racks <span className="inventory-count">{inventory.issues?.Rack ? 'unknown' : racks.length}</span></Title>
    {inventory.issues?.Rack ? <p>Rack details unavailable. The facility's rack count is unknown.</p> : racks.length ? <div className="inventory-scroll inventory-fit"><Table aria-label="Facility racks"><TableHeader><TableRow><TableHead>Rack</TableHead><TableHead>Height</TableHead><TableHead>Equipment records</TableHead></TableRow></TableHeader><TableBody>{racks.map(rack => <TableRow key={slotKey(rack.metadata.namespace, rack.metadata.name)}><TableCell><Link to={inventoryHref('Rack', rack.metadata.namespace, rack.metadata.name)}>{rack.spec.displayName || rack.metadata.name}</Link></TableCell><TableCell>{rack.spec.heightUnits ? `${rack.spec.heightUnits}U` : 'Not recorded'}</TableCell><TableCell>{incomplete ? 'Incomplete' : equipment.filter(slot => slot.record.metadata.namespace === rack.metadata.namespace && slot.placement.rackRef?.name === rack.metadata.name).length}</TableCell></TableRow>)}</TableBody></Table></div> : <p className="inventory-muted">No racks recorded in this facility.</p>}
  </section>;
}

/** Equipment recorded at the facility without a rack. */
export function FacilityUnrackedEquipment({ facility, inventory }: { facility: PhysicalRecord; inventory: Inventory }) {
  const slots = facilityOnly(facility, inventory);
  return slots.length ? <Equipment slots={slots} devicesDenied={!!inventory.issues?.Device} inventory={inventory} /> : inventory.issues?.DeviceClaim || inventory.issues?.Device ? <p>Equipment details unavailable.</p> : <p className="inventory-muted">No facility-only reports or claims recorded.</p>;
}

/** Facility-only equipment count, for the tab label. */
export function unrackedCount(facility: PhysicalRecord, inventory: Inventory) {
  return facilityOnly(facility, inventory).length;
}
