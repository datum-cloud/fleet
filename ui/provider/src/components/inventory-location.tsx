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

function RackFace({ occupants, height, side, selection }: { occupants: RackOccupant[]; height: number; side: RackFaceName; selection?: RackSelection }) {
  const plans = faceOccupancy(occupants, side)
    .sort((first, second) => first.position - second.position || first.entry.key.localeCompare(second.entry.key));
  const laneEnds: number[] = [];
  const placed = plans.map(plan => {
    let lane = laneEnds.findIndex(end => end < plan.position);
    if (lane === -1) lane = laneEnds.length;
    laneEnds[lane] = plan.position + plan.height - 1;
    return { ...plan, lane };
  });
  return <div><h4>{side === 'front' ? 'Front' : 'Rear'}</h4>
    <ol className="inventory-rack-units" aria-label={`${side} recorded placements`} style={{ gridTemplateRows: `repeat(${height}, 16px)`, gridTemplateColumns: `28px repeat(${Math.max(1, laneEnds.length)}, minmax(0, 1fr))` }}>
      {Array.from({ length: height }, (_, index) => height - index).map(unit => {
        const recorded = plans.some(plan => unit >= plan.position && unit < plan.position + plan.height);
        return <li key={unit} className="inventory-rack-unit" style={{ gridRow: height - unit + 1, gridColumn: '1 / -1' }}><span>U{unit}</span>{!recorded && <span className="inventory-muted" aria-label="Occupancy unverified">—</span>}</li>;
      })}
      {placed.map(plan => {
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
          <RackSlotDevice slot={plan.entry} name={recordName} range={range} height={plan.height} label={label} side={side} overlap={overlap} far={plan.far} depthUnknown={plan.depth.source === 'unknown'} to={selection ? selection.href(plan.entry) : inventoryHref(plan.entry.kind, record.metadata.namespace, record.metadata.name)} selected={selected} />
        </li>;
      })}
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
      <p className="inventory-muted">Reported, not verified. Blank units are not free space. Full-depth equipment shows on both faces, muted where its chassis rear shows; half-depth only on its mounted face. Unknown depth is drawn as full depth. ! means records disagree: placements overlap on the same face.</p>
      {denied ? <p>Installation details unavailable. Rack positions cannot be shown.</p> : !height ? <p>Rack height is not recorded. Equipment remains listed without invented slots.</p> : <div className="inventory-rack-faces">{(['front', 'rear'] as const).filter(value => face === 'both' || face === value).map(side => <RackFace key={side} occupants={occupants} height={height} side={side} selection={selection} />)}</div>}
    </aside>
  </section>;
}

export function FacilityOverview({ facility, inventory }: { facility: PhysicalRecord; inventory: Inventory }) {
  const racks = inventory.physical.filter(record => record.kind === 'Rack' && record.metadata.namespace === facility.metadata.namespace && record.spec.facilityRef?.name === facility.metadata.name);
  const equipment = equipmentPlacements(inventory);
  const facilityOnly = equipment.filter(slot => slot.record.metadata.namespace === facility.metadata.namespace && !slot.placement.rackRef && slot.placement.facilityRef?.name === facility.metadata.name);
  return <section className="inventory-equipment-sections" aria-label="Facility contents"><Title as="h3" level={5}>Racks</Title>{inventory.issues?.Rack ? <p>Rack details unavailable. The facility's rack count is unknown.</p> : racks.length ? <div className="inventory-scroll"><Table aria-label="Facility racks"><TableHeader><TableRow><TableHead>Rack</TableHead><TableHead>Recorded height</TableHead><TableHead>Equipment reports / claim fallback</TableHead><TableHead>Available space</TableHead></TableRow></TableHeader><TableBody>{racks.map(rack => <TableRow key={slotKey(rack.metadata.namespace, rack.metadata.name)}><TableCell><Link to={inventoryHref('Rack', rack.metadata.namespace, rack.metadata.name)}>{rack.spec.displayName || rack.metadata.name}</Link></TableCell><TableCell>{rack.spec.heightUnits ? `${rack.spec.heightUnits}U` : 'Not recorded'}</TableCell><TableCell>{inventory.issues?.DeviceClaim || inventory.issues?.Device ? 'Incomplete' : equipment.filter(slot => slot.record.metadata.namespace === rack.metadata.namespace && slot.placement.rackRef?.name === rack.metadata.name).length}</TableCell><TableCell>Not verified</TableCell></TableRow>)}</TableBody></Table></div> : <p className="inventory-muted">No racks recorded in this facility.</p>}
    <Title as="h3" level={5}>Equipment without a recorded rack</Title>{facilityOnly.length ? <Equipment slots={facilityOnly} devicesDenied={!!inventory.issues?.Device} inventory={inventory} /> : inventory.issues?.DeviceClaim || inventory.issues?.Device ? <p>Equipment details unavailable.</p> : <p className="inventory-muted">No facility-only reports or claims recorded.</p>}
  </section>;
}
