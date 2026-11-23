// The dense equipment table, shared by the Rack page's Equipment tab and the
// Facility page's equipment without a rack. One row per recorded placement;
// a row that needs checking carries the elevation's colour edge.

import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { Link } from 'react-router';
import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import { modelLabel, type Inventory } from '../inventory';
import { deviceConnections, portTiles, type LinkState } from '../lib/device-connections';
import { classifyFeeds, FEED_CLASS_WORDS, psuFeeds } from '../lib/power-paths';
import { equipmentGeometry, type EquipmentPlacement } from '../lib/device-placement';
import { displayLabel } from '../lib/display-names';
import { equipmentDepth } from '../lib/device-depth';
import { deviceModelLabel } from '../lib/hardware-catalog';
import { inventoryHref } from './inventory-location';

/** Where a row sits: "U40", "U20–U21", the mount name, or no position. */
function positionLabel(slot: EquipmentPlacement, rack: PhysicalRecord) {
  const geometry = equipmentGeometry(slot, rack);
  if (geometry) return geometry.height === 1 ? `U${geometry.position}` : `U${geometry.position}–U${geometry.position + geometry.height - 1}`;
  if (slot.placement.scope === 'NonRackUnitMount') return slot.placement.mount ?? 'Mount not recorded';
  return 'No position';
}

/** Top of rack first, then equipment without a usable position, then non-unit mounts. */
function rowOrder(slot: EquipmentPlacement, rack: PhysicalRecord) {
  const geometry = equipmentGeometry(slot, rack);
  if (geometry) return geometry.position + geometry.height - 1;
  return slot.placement.scope === 'NonRackUnitMount' ? -2 : -1;
}

/** A device's ports: cabled of total, and how many disagree or reach an unreadable record. */
function portSummary(device: Device, inventory: Inventory) {
  const join = deviceConnections(device, inventory);
  if (join.cableAccess === 'denied') return { text: '?', attention: undefined };
  const tiles = portTiles(join.links).filter(tile => tile.state !== 'catalog');
  const count = (state: LinkState) => tiles.filter(tile => tile.state === state).length;
  const conflict = count('conflict');
  const unavailable = count('unavailable');
  return {
    text: tiles.length ? `${count('cabled') + conflict + unavailable} / ${tiles.length}` : '—',
    attention: conflict ? 'conflict' as const : unavailable ? 'unavailable' as const : undefined,
    title: [conflict && `${conflict} ${conflict === 1 ? 'port disagrees' : 'ports disagree'}`, unavailable && `${unavailable} ${unavailable === 1 ? 'peer unavailable' : 'peers unavailable'}`].filter(Boolean).join(' · ') || undefined,
  };
}

/** Which feeds a device's PSUs reach: "Feed A + Feed B", "Feed A" (single, marked) or "?". */
function powerSummary(device: Device, inventory: Inventory) {
  const psus = psuFeeds(device, inventory);
  if (!psus?.length) return psus ? { text: '—' } : { text: '?' };
  const classified = classifyFeeds(psus);
  const labels = [...new Set(psus.flatMap(psu => psu.feed ? [psu.feed.label] : []))];
  const unknown = psus.some(psu => !psu.feed);
  return { text: [...labels, ...(unknown ? ['?'] : [])].join(' + ') || '?', single: classified?.class === 'single', title: classified ? FEED_CLASS_WORDS[classified.class] : undefined };
}

/** The installation plan behind a row: its link, unavailable, or amber "Plan not found" naming the missing claim. */
function PlanCell({ slot, inventory }: { slot: EquipmentPlacement; inventory: Inventory }) {
  if (slot.claim) return <Link to={inventoryHref('DeviceClaim', slot.claim.metadata.namespace, slot.claim.metadata.name)}>{displayLabel(slot.claim)}</Link>;
  if (inventory.issues?.DeviceClaim) return <span className="inventory-muted">Unavailable</span>;
  const missing = slot.kind === 'Device' ? (slot.record as Device).spec.claimRef?.name : undefined;
  if (!missing) return <>—</>;
  const words = `Installation plan ${missing} not found`;
  return <span data-tone="warning" title={words}>Plan not found<span className="inventory-visually-hidden"> · {words}</span></span>;
}

/**
 * Recorded placements in one dense table: position (in a rack), device,
 * model, face, depth, cabled ports, the feeds its PSUs reach, serial and
 * asset tag when any row records them, and the installation plan when asked.
 * In a rack, rows run top of rack first; otherwise by device name.
 */
export function EquipmentTable({ slots, rack, inventory, label = 'Rack equipment', plan = false }: { slots: EquipmentPlacement[]; rack?: PhysicalRecord; inventory: Inventory; label?: string; plan?: boolean }) {
  const devicesDenied = !!inventory.issues?.Device;
  const sorted = rack ? [...slots].sort((a, b) => rowOrder(b, rack) - rowOrder(a, rack)) : [...slots].sort((a, b) => displayLabel(a.devices[0] ?? a.record).localeCompare(displayLabel(b.devices[0] ?? b.record)));
  const rows = sorted.map(slot => {
    const device = !devicesDenied && slot.devices.length === 1 ? slot.devices[0] : undefined;
    const depth = equipmentDepth(slot, inventory);
    return {
      slot, device,
      ports: device ? portSummary(device, inventory) : undefined,
      power: device ? powerSummary(device, inventory) : undefined,
      depth: depth.source === 'unknown' ? 'Unknown' : depth.full ? 'Full' : 'Half',
    };
  });
  const serials = rows.some(row => row.device?.spec.identity.serial);
  const tags = rows.some(row => row.device?.spec.assetTag);
  return <div className="inventory-scroll"><Table aria-label={label} className="inventory-dense-table">
    <TableHeader><TableRow>
      {rack && <TableHead>U</TableHead>}<TableHead>Device</TableHead><TableHead>Model</TableHead><TableHead>Face</TableHead><TableHead>Depth</TableHead><TableHead>Ports</TableHead><TableHead>Power</TableHead>
      {serials && <TableHead>Serial</TableHead>}
      {tags && <TableHead>Asset tag</TableHead>}
      {plan && <TableHead>Plan</TableHead>}
    </TableRow></TableHeader>
    <TableBody>{rows.map(({ slot, device, ports, power, depth }) => <TableRow key={slot.key} data-attention={ports?.attention}>
      {rack && <TableCell className="inventory-numeric">{positionLabel(slot, rack)}</TableCell>}
      <TableCell>
        {devicesDenied ? 'Unavailable' : slot.devices.length ? slot.devices.map(item => <Link key={item.metadata.name} to={inventoryHref('Device', item.metadata.namespace, item.metadata.name)}>{displayLabel(item)}</Link>) : <span className="inventory-muted" title="Installation plan with no device registered">{displayLabel(slot.record)}</span>}
        {device?.spec.lifecycle && device.spec.lifecycle !== 'Active' && <span className="inventory-muted"> · {device.spec.lifecycle}</span>}
        {slot.devices.length > 1 && <span className="inventory-muted" title="Multiple registrations; the active device is unknown"> · {slot.devices.length} registrations</span>}
      </TableCell>
      <TableCell>{device ? deviceModelLabel(device, inventory) : slot.claim ? <span className="inventory-muted">{modelLabel(slot.claim.spec.expectedIdentity)}</span> : '—'}</TableCell>
      <TableCell>{slot.placement.face ?? '—'}</TableCell>
      <TableCell>{depth}</TableCell>
      <TableCell className="inventory-numeric" title={ports?.title}>{ports?.text ?? '—'}</TableCell>
      <TableCell title={power?.title} data-single={power?.single ? '' : undefined}>{power?.text ?? '—'}</TableCell>
      {serials && <TableCell className="inventory-numeric">{device?.spec.identity.serial}</TableCell>}
      {tags && <TableCell className="inventory-numeric">{device?.spec.assetTag}</TableCell>}
      {plan && <TableCell><PlanCell slot={slot} inventory={inventory} /></TableCell>}
    </TableRow>)}</TableBody>
  </Table></div>;
}
