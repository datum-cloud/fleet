import { Button } from '@datum-cloud/datum-ui/button';
import { ButtonGroup } from '@datum-cloud/datum-ui/button-group';
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@datum-cloud/datum-ui/table';
import { Title } from '@datum-cloud/datum-ui/typography';
import { useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { slotKey, type Inventory, type InventorySlot } from '../inventory';
import type { Device, PhysicalRecord, RackFace as RackFaceName } from '../../../shared/fleet-api';
import { depthLabel } from '../lib/device-depth';
import { faceOccupancy, RACK_FACES, rackOccupants, type RackOccupant } from '../lib/rack-faces';
import { devicePlacement, equipmentGeometry, equipmentPlacements, placementFacility, type EquipmentPlacement } from '../lib/device-placement';
import { displayLabel } from '../lib/display-names';
import { RackSlotDevice } from './rack-slot-device';
import { EquipmentTable } from './equipment-table';
import { CapacityBar, useTelemetry } from './power-telemetry';

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
      <div className="inventory-elevation-toolbar"><ButtonGroup className="inventory-face-switch" aria-label="Rack faces">{(['both', 'front', 'rear'] as const).map(value => <Button type={face === value ? 'primary' : 'secondary'} theme={face === value ? 'light' : 'outline'} size="small" key={value} htmlType="button" aria-pressed={face === value} onClick={() => setFace(value)}>{value === 'both' ? 'Front + rear' : value === 'front' ? 'Front' : 'Rear'}</Button>)}</ButtonGroup>{toolbar}</div>
      {denied ? <p>Installation details unavailable. Rack positions cannot be shown.</p> : !height ? <p>Rack height is not recorded. Equipment remains listed without invented slots.</p> : <div className="inventory-rack-faces">{(['front', 'rear'] as const).filter(value => face === 'both' || face === value).map(side => <RackFace key={side} occupants={occupants} height={height} side={side} selection={selection} inventory={inventory} unit={UNIT_PX} />)}</div>}
    </aside>
  </section>;
}

const facilityRacks = (facility: PhysicalRecord, inventory: Inventory) => inventory.physical.filter(record => record.kind === 'Rack' && record.metadata.namespace === facility.metadata.namespace && record.spec.facilityRef?.name === facility.metadata.name);
const facilityOnly = (facility: PhysicalRecord, inventory: Inventory) => equipmentPlacements(inventory).filter(slot => slot.record.metadata.namespace === facility.metadata.namespace && !slot.placement.rackRef && slot.placement.facilityRef?.name === facility.metadata.name);

/** A rack's placements that need checking: red when they overlap on a face, amber when some have no usable position. */
function rackAttention(rack: PhysicalRecord, slots: EquipmentPlacement[], inventory: Inventory): { attention?: 'conflict' | 'unavailable'; note?: string } {
  const occupants = rackOccupants(slots.filter(slot => equipmentGeometry(slot, rack)), rack, inventory);
  if (RACK_FACES.some(side => faceOccupancy(occupants, side).some(occupant => occupant.conflicts.length))) return { attention: 'conflict', note: 'Placements disagree: overlapping units on a face' };
  const unpositioned = slots.filter(slot => !equipmentGeometry(slot, rack) && slot.placement.scope !== 'NonRackUnitMount').length;
  return unpositioned ? { attention: 'unavailable', note: `${unpositioned} without a usable unit position` } : {};
}

/** The feeds that name a rack, each as the Rack page's compact load and percentage. */
function RackPower({ rack, inventory }: { rack: PhysicalRecord; inventory: Inventory }) {
  const { source, now } = useTelemetry();
  if (inventory.issues?.PowerFeed) return <span title="Power feed records unavailable">?</span>;
  const feeds = inventory.physical.filter(record => record.kind === 'PowerFeed' && record.metadata.namespace === rack.metadata.namespace && record.spec.rackRef?.name === rack.metadata.name);
  if (!feeds.length) return <>—</>;
  return <span className="inventory-rack-power">{feeds.map(feed => <CapacityBar key={feed.metadata.name} feed={feed} reading={source.read({ kind: 'PowerFeed', namespace: feed.metadata.namespace, name: feed.metadata.name })} now={now} source={source} compact />)}</span>;
}

/** Racks in one dense table: Rack | Height | Equipment | Power. A rack whose placements need checking carries the elevation's colour edge. */
export function RacksTable({ racks, inventory, label = 'Racks' }: { racks: PhysicalRecord[]; inventory: Inventory; label?: string }) {
  const incomplete = !!inventory.issues?.DeviceClaim || !!inventory.issues?.Device;
  return <div className="inventory-scroll"><Table aria-label={label} className="inventory-dense-table">
    <TableHeader><TableRow><TableHead>Rack</TableHead><TableHead>Height</TableHead><TableHead>Equipment</TableHead><TableHead>Power</TableHead></TableRow></TableHeader>
    <TableBody>{racks.map(rack => {
      const slots = rackPlacements(rack, inventory);
      const { attention, note } = rackAttention(rack, slots, inventory);
      return <TableRow key={slotKey(rack.metadata.namespace, rack.metadata.name)} data-attention={attention}>
        <TableCell title={note}><Link to={inventoryHref('Rack', rack.metadata.namespace, rack.metadata.name)}>{displayLabel(rack)}</Link>{note && <span className="inventory-visually-hidden"> · {note}</span>}</TableCell>
        <TableCell className="inventory-numeric">{rack.spec.heightUnits ? `${rack.spec.heightUnits}U` : '?'}</TableCell>
        <TableCell className="inventory-numeric">{inventory.issues?.DeviceClaim && !slots.length ? '?' : incomplete ? <span data-tone="warning" title="Some equipment records are unavailable">{slots.length}+</span> : slots.length}</TableCell>
        <TableCell><RackPower rack={rack} inventory={inventory} /></TableCell>
      </TableRow>;
    })}</TableBody>
  </Table></div>;
}

/** A Facility's racks. An unreadable rack collection is unknown, never empty. */
export function FacilityRacks({ facility, inventory }: { facility: PhysicalRecord; inventory: Inventory }) {
  const racks = facilityRacks(facility, inventory);
  return inventory.issues?.Rack ? <p>Rack details unavailable.</p> : racks.length ? <RacksTable racks={racks} inventory={inventory} label="Facility racks" /> : <p className="inventory-muted">No racks recorded.</p>;
}

/** Facility rack count, for the tab label. */
export function facilityRackCount(facility: PhysicalRecord, inventory: Inventory) {
  return facilityRacks(facility, inventory).length;
}

/** Equipment recorded at the facility without a rack, in the rack equipment table's columns. */
export function FacilityUnrackedEquipment({ facility, inventory }: { facility: PhysicalRecord; inventory: Inventory }) {
  const slots = facilityOnly(facility, inventory);
  return slots.length ? <EquipmentTable slots={slots} inventory={inventory} label="Equipment without a rack" plan /> : inventory.issues?.DeviceClaim || inventory.issues?.Device ? <p>Equipment details unavailable.</p> : <p className="inventory-muted">No equipment recorded without a rack.</p>;
}

/** Facility-only equipment count, for the tab label. */
export function unrackedCount(facility: PhysicalRecord, inventory: Inventory) {
  return facilityOnly(facility, inventory).length;
}

/** Where a device is, as "Rack · U12": the rack and units, the mount, or the facility when it has no rack. */
function locationOf(device: Device, inventory: Inventory, withFacility: boolean): { text: ReactNode; sort: string; note?: string } {
  const entry = devicePlacement(device, inventory);
  if (!entry) return { text: '?', sort: '~', note: inventory.issues?.DeviceClaim ? 'Installation plans unavailable; location unknown' : 'Location unknown: no reported placement or installation plan' };
  const { placement } = entry;
  const namespace = device.metadata.namespace;
  const named = (kind: 'Facility' | 'Rack', name: string) => {
    if (inventory.issues?.[kind]) return { node: `${kind} unavailable`, label: '~' };
    const record = inventory.physical.find(item => item.kind === kind && item.metadata.namespace === namespace && item.metadata.name === name);
    return record ? { node: <Link to={inventoryHref(kind, namespace, name)}>{displayLabel(record)}</Link>, label: displayLabel(record) } : { node: `${name} (not found)`, label: name };
  };
  const facility = placementFacility(placement, namespace, inventory);
  const parts: { node: ReactNode; label: string }[] = [];
  if (withFacility || !placement.rackRef) parts.push(facility ? named('Facility', facility) : { node: 'Facility unknown', label: '~' });
  if (placement.rackRef) parts.push(named('Rack', placement.rackRef.name));
  const height = placement.height ?? 1;
  const units = placement.mount ? `Mount: ${placement.mount}` : placement.position !== undefined ? `U${placement.position}${height > 1 ? `–U${placement.position + height - 1}` : ''}` : placement.rackRef ? 'U?' : undefined;
  const notes = [
    entry.basis === 'Claim-derived fallback' ? "Planned: no placement report; this is the installation plan's location" : undefined,
    placement.rackRef && placement.position === undefined && !placement.mount ? 'Position in the rack not recorded' : undefined,
  ].filter(Boolean);
  return {
    text: <>{parts.map((part, index) => <span key={index}>{index > 0 && ' · '}{part.node}</span>)}{units && <>{parts.length > 0 && ' · '}{units}</>}</>,
    sort: `${parts.map(part => part.label).join('/')}/${String(placement.position ?? 0).padStart(4, '0')}`,
    note: notes.join(' · ') || undefined,
  };
}

/**
 * Devices in one dense table: Device | Rack · U | Model | Serial | Asset tag
 * | Lifecycle, sorted by location. Model appears when asked; serial, asset
 * tag and lifecycle only when some row has a value (lifecycle when some is
 * not Active). A planned-only or unknown location carries an amber edge.
 */
export function DeviceTable({ devices, inventory, label, model, withFacility = false }: { devices: Device[]; inventory: Inventory; label: string; model?: (device: Device) => ReactNode; withFacility?: boolean }) {
  const rows = devices.map(device => ({ device, location: locationOf(device, inventory, withFacility) }))
    .sort((a, b) => a.location.sort.localeCompare(b.location.sort) || displayLabel(a.device).localeCompare(displayLabel(b.device)));
  const serials = devices.some(device => device.spec.identity.serial);
  const tags = devices.some(device => device.spec.assetTag);
  const lifecycles = devices.some(device => device.spec.lifecycle && device.spec.lifecycle !== 'Active');
  return <div className="inventory-scroll" tabIndex={0} role="region" aria-label={label}><Table aria-label={label} className="inventory-dense-table">
    <TableHeader><TableRow>
      <TableHead>Device</TableHead><TableHead>{withFacility ? 'Location' : 'Rack · U'}</TableHead>
      {model && <TableHead>Model</TableHead>}
      {serials && <TableHead>Serial</TableHead>}
      {tags && <TableHead>Asset tag</TableHead>}
      {lifecycles && <TableHead>Lifecycle</TableHead>}
    </TableRow></TableHeader>
    <TableBody>{rows.map(({ device, location }) => <TableRow key={slotKey(device.metadata.namespace, device.metadata.name)} data-attention={location.note ? 'unavailable' : undefined}>
      <TableCell><Link to={inventoryHref('Device', device.metadata.namespace, device.metadata.name)}>{displayLabel(device)}</Link></TableCell>
      <TableCell title={location.note}>{location.text}{location.note && <span className="inventory-visually-hidden"> · {location.note}</span>}</TableCell>
      {model && <TableCell>{model(device)}</TableCell>}
      {serials && <TableCell className="inventory-numeric">{device.spec.identity.serial}</TableCell>}
      {tags && <TableCell className="inventory-numeric">{device.spec.assetTag}</TableCell>}
      {lifecycles && <TableCell>{device.spec.lifecycle && device.spec.lifecycle !== 'Active' ? device.spec.lifecycle : ''}</TableCell>}
    </TableRow>)}</TableBody>
  </Table></div>;
}
