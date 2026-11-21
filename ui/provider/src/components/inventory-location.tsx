import { useState } from 'react';
import { Link } from 'react-router';
import { slotKey, type Inventory, type InventorySlot } from '../inventory';
import type { PhysicalRecord } from '../../../shared/fleet-api';

export function inventoryHref(kind: string, namespace?: string, name?: string) {
  const params = new URLSearchParams({ kind });
  if (name !== undefined) { params.set('record', name); params.set('namespace', namespace ?? ''); }
  return `?${params}`;
}

export function rackGeometry(slot: InventorySlot, rack: PhysicalRecord) {
  const placement = slot.claim.spec.placement;
  const { position, height, face } = placement;
  if (placement.scope !== 'RackUnits' || !Number.isInteger(position) || !Number.isInteger(height) || !position || !height || position < 1 || height < 1 || !rack.spec.heightUnits || position + height - 1 > rack.spec.heightUnits || (face !== 'front' && face !== 'rear')) return undefined;
  return { position, height, face };
}

function Equipment({ slots, devicesDenied }: { slots: InventorySlot[]; devicesDenied: boolean }) {
  return <div className="inventory-scroll"><table aria-label="Recorded equipment"><thead><tr><th>Equipment</th><th>Installation plan</th><th>Recorded placement</th></tr></thead><tbody>
    {slots.map(slot => <tr key={slot.key}><td>{devicesDenied ? 'Device details unavailable' : slot.devices.length ? slot.devices.map(device => <div key={device.metadata.name}><Link to={inventoryHref('Device', device.metadata.namespace, device.metadata.name)}>{device.spec.displayName || device.metadata.name}</Link></div>) : 'No device registration recorded'}{slot.devices.length > 1 && <small>Multiple registrations · active device unknown</small>}</td><td><Link to={inventoryHref('DeviceClaim', slot.claim.metadata.namespace, slot.claim.metadata.name)}>{slot.claim.spec.displayName || slot.claim.metadata.name}</Link></td><td>{slot.claim.spec.placement.scope === 'NonRackUnitMount' ? slot.claim.spec.placement.mount || 'Mount not recorded' : slot.claim.spec.placement.scope === 'RackUnits' ? `U${slot.claim.spec.placement.position ?? '?'} · ${slot.claim.spec.placement.height ?? '?'}U · ${slot.claim.spec.placement.face ?? 'Face unknown'}` : 'Position not recorded'}</td></tr>)}
  </tbody></table></div>;
}

function RackFace({ rack, slots, side }: { rack: PhysicalRecord; slots: InventorySlot[]; side: 'front' | 'rear' }) {
  const height = rack.spec.heightUnits!;
  const plans = slots.map(slot => ({ slot, ...rackGeometry(slot, rack)! }))
    .filter(plan => plan.face === side)
    .sort((first, second) => first.position - second.position || first.slot.key.localeCompare(second.slot.key));
  const laneEnds: number[] = [];
  const placed = plans.map(plan => {
    let lane = laneEnds.findIndex(end => end < plan.position);
    if (lane === -1) lane = laneEnds.length;
    laneEnds[lane] = plan.position + plan.height - 1;
    return { ...plan, lane };
  });
  return <div><h4>{side === 'front' ? 'Front' : 'Rear'}</h4>
    <ol className="inventory-rack-units" aria-label={`${side} recorded plans`} style={{ gridTemplateRows: `repeat(${height}, 16px)`, gridTemplateColumns: `28px repeat(${Math.max(1, laneEnds.length)}, minmax(0, 1fr))` }}>
      {Array.from({ length: height }, (_, index) => height - index).map(unit => {
        const recorded = plans.some(plan => unit >= plan.position && unit < plan.position + plan.height);
        return <li key={unit} className="inventory-rack-unit" style={{ gridRow: height - unit + 1, gridColumn: '1 / -1' }}><span>U{unit}</span>{!recorded && <span className="inventory-muted" aria-label="Occupancy unverified">—</span>}</li>;
      })}
      {placed.map(plan => {
        const top = plan.position + plan.height - 1;
        const overlap = plans.some(other => other.slot.key !== plan.slot.key && other.position <= top && other.position + other.height > plan.position);
        const name = plan.slot.claim.spec.displayName || plan.slot.claim.metadata.name;
        const range = plan.height === 1 ? `U${plan.position}` : `U${plan.position}–U${top}`;
        const label = `${name} · ${range} · ${plan.height}U${overlap ? ' · overlapping plans, review required' : ''}`;
        return <li key={plan.slot.key} className="inventory-rack-plan" style={{ gridRow: `${height - top + 1} / span ${plan.height}`, gridColumn: plan.lane + 2 }}>
          <Link title={label} to={inventoryHref('DeviceClaim', plan.slot.claim.metadata.namespace, plan.slot.claim.metadata.name)} aria-label={`Review ${label}, ${side}`}>{name}{plan.height > 1 && <small>{range} · {plan.height}U</small>}{overlap && <small>Overlapping plans</small>}</Link>
        </li>;
      })}
    </ol>
  </div>;
}

export function RackOverview({ rack, inventory }: { rack: PhysicalRecord; inventory: Inventory }) {
  const [face, setFace] = useState<'both' | 'front' | 'rear'>('both');
  const slots = inventory.slots.filter(slot => slot.claim.metadata.namespace === rack.metadata.namespace && slot.claim.spec.placement.rackRef?.name === rack.metadata.name);
  const positioned = slots.filter(slot => rackGeometry(slot, rack));
  const nonRacked = slots.filter(slot => slot.claim.spec.placement.scope === 'NonRackUnitMount');
  const unpositioned = slots.filter(slot => !rackGeometry(slot, rack) && slot.claim.spec.placement.scope !== 'NonRackUnitMount');
  const denied = !!inventory.issues?.DeviceClaim;
  const devicesDenied = !!inventory.issues?.Device;
  const height = rack.spec.heightUnits;
  return <section className="inventory-rack-layout" aria-label="Rack layout">
    <aside className="inventory-elevation">
      <div className="inventory-list-heading"><h3>Rack elevation</h3><span>{height ? `${height}U` : 'Height unknown'}</span></div>
      <div className="inventory-face-switch" role="group" aria-label="Rack faces">{(['both', 'front', 'rear'] as const).map(value => <button key={value} type="button" aria-pressed={face === value} onClick={() => setFace(value)}>{value === 'both' ? 'Front + rear' : value === 'front' ? 'Front' : 'Rear'}</button>)}</div>
      <p className="inventory-muted">Recorded installation plans, not verified occupancy. Blank units are not available space.</p>
      {denied ? <p>Installation details unavailable. Rack positions cannot be shown.</p> : !height ? <p>Rack height is not recorded. Equipment remains listed without invented slots.</p> : <div className="inventory-rack-faces">{(['front', 'rear'] as const).filter(value => face === 'both' || face === value).map(side => <RackFace key={side} rack={rack} slots={positioned} side={side} />)}</div>}
    </aside>
    <div className="inventory-equipment-sections">
      <h3>Equipment & installation plans</h3>
      {denied ? <p>Installation details unavailable. This is not an empty rack.</p> : <>
        <section><h4>Positioned plans <span className="inventory-count">{positioned.length}</span></h4>{positioned.length ? <Equipment slots={positioned} devicesDenied={devicesDenied} /> : <p className="inventory-muted">No complete in-range unit positions recorded.</p>}</section>
        <section><h4>Unpositioned equipment <span className="inventory-count">{unpositioned.length}</span></h4>{unpositioned.length ? <Equipment slots={unpositioned} devicesDenied={devicesDenied} /> : <p className="inventory-muted">No unpositioned plans recorded for this rack.</p>}</section>
        <section><h4>Non-racked mounts <span className="inventory-count">{nonRacked.length}</span></h4>{nonRacked.length ? <Equipment slots={nonRacked} devicesDenied={devicesDenied} /> : <p className="inventory-muted">No side, rail, or other non-unit mounts recorded.</p>}</section>
        {!slots.length && <p className="inventory-muted">No related records found. This does not establish that equipment is absent.</p>}
      </>}
    </div>
  </section>;
}

export function FacilityOverview({ facility, inventory }: { facility: PhysicalRecord; inventory: Inventory }) {
  const racks = inventory.physical.filter(record => record.kind === 'Rack' && record.metadata.namespace === facility.metadata.namespace && record.spec.facilityRef?.name === facility.metadata.name);
  const facilityOnly = inventory.slots.filter(slot => slot.claim.metadata.namespace === facility.metadata.namespace && slot.claim.spec.placement.scope === 'Facility' && slot.claim.spec.placement.facilityRef?.name === facility.metadata.name);
  return <section className="inventory-equipment-sections" aria-label="Facility contents"><h3>Racks</h3>{inventory.issues?.Rack ? <p>Rack details unavailable. The facility's rack count is unknown.</p> : racks.length ? <div className="inventory-scroll"><table aria-label="Facility racks"><thead><tr><th>Rack</th><th>Recorded height</th><th>Installation plans</th><th>Available space</th></tr></thead><tbody>{racks.map(rack => <tr key={slotKey(rack.metadata.namespace, rack.metadata.name)}><td><Link to={inventoryHref('Rack', rack.metadata.namespace, rack.metadata.name)}>{rack.spec.displayName || rack.metadata.name}</Link></td><td>{rack.spec.heightUnits ? `${rack.spec.heightUnits}U` : 'Not recorded'}</td><td>{inventory.issues?.DeviceClaim ? 'Unavailable' : inventory.slots.filter(slot => slot.claim.metadata.namespace === rack.metadata.namespace && slot.claim.spec.placement.rackRef?.name === rack.metadata.name).length}</td><td>Not verified</td></tr>)}</tbody></table></div> : <p className="inventory-muted">No racks recorded in this facility.</p>}
    <h3>Equipment without a recorded rack</h3>{inventory.issues?.DeviceClaim ? <p>Installation details unavailable.</p> : facilityOnly.length ? <Equipment slots={facilityOnly} devicesDenied={!!inventory.issues?.Device} /> : <p className="inventory-muted">No facility-only installation plans recorded.</p>}
  </section>;
}
