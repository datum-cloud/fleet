import { Button } from '@datum-cloud/datum-ui/button';
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@datum-cloud/datum-ui/table';
import { Title } from '@datum-cloud/datum-ui/typography';
import { useState } from 'react';
import { Link } from 'react-router';
import { slotKey, type Inventory, type InventorySlot } from '../inventory';
import type { PhysicalRecord } from '../../../shared/fleet-api';
import { equipmentGeometry, equipmentPlacements, reportedPlacementLabel, type EquipmentPlacement } from '../lib/device-placement';

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

function RackFace({ rack, slots, side }: { rack: PhysicalRecord; slots: EquipmentPlacement[]; side: 'front' | 'rear' }) {
  const height = rack.spec.heightUnits!;
  const plans = slots.map(slot => ({ slot, ...equipmentGeometry(slot, rack)! }))
    .filter(plan => plan.face === side || plan.fullDepth === true)
    .sort((first, second) => first.position - second.position || first.slot.key.localeCompare(second.slot.key));
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
        const overlap = plans.some(other => other.slot.key !== plan.slot.key && other.position <= top && other.position + other.height > plan.position);
        const name = plan.slot.record.spec.displayName || plan.slot.record.metadata.name;
        const range = plan.height === 1 ? `U${plan.position}` : `U${plan.position}–U${top}`;
        const label = `${name} · ${range} · ${plan.height}U · ${plan.slot.basis}${overlap ? ' · overlapping placements, review required' : ''}`;
        return <li key={plan.slot.key} className="inventory-rack-plan" style={{ gridRow: `${height - top + 1} / span ${plan.height}`, gridColumn: plan.lane + 2 }}>
          <Link title={label} to={inventoryHref(plan.slot.kind, plan.slot.record.metadata.namespace, plan.slot.record.metadata.name)} aria-label={`Review ${label}, ${side}`}><span className="inventory-rack-plan-heading">{overlap && <span className="inventory-rack-overlap" aria-hidden="true">!</span>}<span className="inventory-rack-plan-name">{name}</span></span>{plan.height > 1 && <small>{range} · {plan.height}U</small>}</Link>
        </li>;
      })}
    </ol>
  </div>;
}

export function RackOverview({ rack, inventory }: { rack: PhysicalRecord; inventory: Inventory }) {
  const [face, setFace] = useState<'both' | 'front' | 'rear'>('both');
  const slots = equipmentPlacements(inventory).filter(slot => slot.record.metadata.namespace === rack.metadata.namespace && slot.placement.rackRef?.name === rack.metadata.name);
  const positioned = slots.filter(slot => equipmentGeometry(slot, rack));
  const nonRacked = slots.filter(slot => slot.placement.scope === 'NonRackUnitMount');
  const unpositioned = slots.filter(slot => !equipmentGeometry(slot, rack) && slot.placement.scope !== 'NonRackUnitMount');
  const denied = !!inventory.issues?.DeviceClaim && !slots.length;
  const devicesDenied = !!inventory.issues?.Device;
  const height = rack.spec.heightUnits;
  return <section className="inventory-rack-layout" aria-label="Rack layout">
    <aside className="inventory-elevation">
      <div className="inventory-list-heading"><Title as="h3" level={5}>Rack elevation</Title><span>{height ? `${height}U` : 'Height unknown'}</span></div>
      <div className="inventory-face-switch" role="group" aria-label="Rack faces">{(['both', 'front', 'rear'] as const).map(value => <Button type="secondary" theme={face === value ? 'light' : 'outline'} size="small" key={value} htmlType="button" aria-pressed={face === value} onClick={() => setFace(value)}>{value === 'both' ? 'Front + rear' : value === 'front' ? 'Front' : 'Rear'}</Button>)}</div>
      <p className="inventory-muted">Source reports and explicitly labelled claim-derived fallback, not verified occupancy. Blank units are not available space. ! indicates overlapping placements requiring review. Unknown depth does not establish clearance on the opposite face.</p>
      {denied ? <p>Installation details unavailable. Rack positions cannot be shown.</p> : !height ? <p>Rack height is not recorded. Equipment remains listed without invented slots.</p> : <div className="inventory-rack-faces">{(['front', 'rear'] as const).filter(value => face === 'both' || face === value).map(side => <RackFace key={side} rack={rack} slots={positioned} side={side} />)}</div>}
    </aside>
    <div className="inventory-equipment-sections">
      <Title as="h3" level={5}>Equipment & registration history</Title>
      {(inventory.issues?.Device || inventory.issues?.DeviceClaim) && <p>Some records are unavailable; equipment counts are incomplete.</p>}
      {denied ? <p>Installation details unavailable. This is not an empty rack.</p> : <>
        <section><h4>Positioned equipment <span className="inventory-count">{positioned.length}</span></h4>{positioned.length ? <Equipment slots={positioned} devicesDenied={devicesDenied} inventory={inventory} /> : <p className="inventory-muted">No complete in-range unit positions recorded.</p>}</section>
        <section><h4>Unpositioned equipment <span className="inventory-count">{unpositioned.length}</span></h4>{unpositioned.length ? <Equipment slots={unpositioned} devicesDenied={devicesDenied} inventory={inventory} /> : <p className="inventory-muted">No unpositioned reports or claims recorded for this rack.</p>}</section>
        <section><h4>Non-racked mounts <span className="inventory-count">{nonRacked.length}</span></h4>{nonRacked.length ? <Equipment slots={nonRacked} devicesDenied={devicesDenied} inventory={inventory} /> : <p className="inventory-muted">No side, rail, or other non-unit mounts recorded.</p>}</section>
        {!slots.length && <p className="inventory-muted">No related records found. This does not establish that equipment is absent.</p>}
      </>}
    </div>
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
