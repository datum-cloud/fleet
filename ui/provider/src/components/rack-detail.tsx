// The Rack page: the elevation is the main content, with a selected device's
// faceplate in a side panel (a bottom sheet on phones), and tabs for the
// equipment table, cables, power and record history. The tab and the
// selection live in the query string, next to the existing kind/record/
// namespace detail link, so links and Back restore them.
//
// Honesty rules carried over from the single-page layout: placement is
// reported, never verified; blank units are never free; claim-derived
// fallback stays labelled; overlaps are flagged, never blocked; a denied
// collection shows "unavailable" and names nothing from it.

import { Button } from '@datum-cloud/datum-ui/button';
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { Tabs, TabsLinkTrigger, TabsList } from '@datum-cloud/datum-ui/tabs';
import { Title } from '@datum-cloud/datum-ui/typography';
import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import type { Device, DeviceClaim, PhysicalRecord } from '../../../shared/fleet-api';
import { modelLabel, slotKey, type Inventory } from '../inventory';
import { deviceConnections, linkStateGlyph, linkStateWords, localLabel, peerLabel, stateCounts, type LinkState } from '../lib/device-connections';
import { equipmentGeometry, type EquipmentPlacement } from '../lib/device-placement';
import { displayLabel } from '../lib/display-names';
import { depthLabel, equipmentDepth } from '../lib/device-depth';
import { faceOccupancy, RACK_FACES, rackOccupants, type RackOccupant } from '../lib/rack-faces';
import { deviceModelLabel } from '../lib/hardware-catalog';
import { DeviceFaceplate } from './device-faceplate';
import { inventoryHref, rackPlacements, RackOverview } from './inventory-location';
import { RackPowerRedundancy } from './rack-power';

export const RACK_TABS = [
  { tab: 'elevation', title: 'Elevation' },
  { tab: 'equipment', title: 'Equipment' },
  { tab: 'cables', title: 'Cables' },
  { tab: 'power', title: 'Power' },
  { tab: 'history', title: 'History' },
] as const;
export type RackTab = (typeof RACK_TABS)[number]['tab'];

/** Query keys the rack page owns, beside kind/record/namespace. */
export const RACK_PARAMS = { tab: 'tab', selectedKind: 'selectedKind', selected: 'selected' } as const;

const PHONE = '(max-width: 600px)';

function usePhone() {
  const query = () => typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia(PHONE).matches;
  const [phone, setPhone] = useState(query);
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return;
    const list = window.matchMedia(PHONE);
    const update = () => setPhone(list.matches);
    update();
    list.addEventListener?.('change', update);
    return () => list.removeEventListener?.('change', update);
  }, []);
  return phone;
}


function unitsLabel(slot: EquipmentPlacement, rack: PhysicalRecord) {
  const geometry = equipmentGeometry(slot, rack);
  const { placement } = slot;
  if (geometry) return geometry.height === 1 ? `U${geometry.position} · 1U` : `U${geometry.position}–U${geometry.position + geometry.height - 1} · ${geometry.height}U`;
  if (placement.scope === 'NonRackUnitMount') return `Mount: ${placement.mount ?? 'Not recorded'}`;
  if (placement.position === undefined) return 'Position not recorded';
  return `U${placement.position} recorded · incomplete or outside the rack`;
}

function slotName(slot: EquipmentPlacement) {
  return displayLabel(slot.record);
}

export interface RackIssue { kind: 'overlap' | 'unpositioned'; text: string; slots: EquipmentPlacement[] }

/**
 * Overlapping placements, using the elevation's own face-aware occupancy
 * (rack-faces.ts): same-face overlaps only. Each cluster is reported once.
 */
export function rackIssues(rack: PhysicalRecord, slots: EquipmentPlacement[], inventory: Inventory): RackIssue[] {
  const occupants = rackOccupants(slots.filter(slot => equipmentGeometry(slot, rack)), rack, inventory);
  const clusters = new Map<string, { occupants: RackOccupant[]; faces: Set<string> }>();
  for (const side of RACK_FACES) {
    const here = faceOccupancy(occupants, side);
    const byKey = new Map(here.map(occupant => [occupant.entry.key, occupant]));
    const seen = new Set<string>();
    for (const start of here) {
      if (!start.conflicts.length || seen.has(start.entry.key)) continue;
      const group: RackOccupant[] = [];
      const queue = [start.entry.key];
      while (queue.length) {
        const key = queue.pop()!;
        if (seen.has(key)) continue;
        seen.add(key);
        const occupant = byKey.get(key)!;
        group.push(occupant);
        queue.push(...occupant.conflicts);
      }
      const id = group.map(item => item.entry.key).sort().join('|');
      const entry = clusters.get(id) ?? { occupants: group.sort((a, b) => a.position - b.position), faces: new Set<string>() };
      entry.faces.add(side);
      clusters.set(id, entry);
    }
  }
  const issues: RackIssue[] = [...clusters.values()].map(({ occupants: group, faces }) => {
    const low = Math.min(...group.map(item => item.position));
    const high = Math.max(...group.map(item => item.position + item.height - 1));
    return {
      kind: 'overlap',
      text: `${group.length} placements disagree at ${low === high ? `U${low}` : `U${low}–U${high}`} (${[...faces].join(' and ')}). Flagged, not blocked:`,
      slots: group.map(item => item.entry),
    };
  });
  const unpositioned = slots.filter(slot => !equipmentGeometry(slot, rack) && slot.placement.scope !== 'NonRackUnitMount');
  if (unpositioned.length) issues.push({
    kind: 'unpositioned',
    text: `${unpositioned.length} in this rack without a usable unit position, so not drawn:`,
    slots: unpositioned,
  });
  return issues;
}

function rackCables(rack: PhysicalRecord, slots: EquipmentPlacement[], inventory: Inventory) {
  if (inventory.issues?.Cable || inventory.issues?.Device) return undefined;
  const names = new Set(slots.flatMap(slot => slot.devices.map(device => device.metadata.name)));
  return inventory.physical.filter(record => record.kind === 'Cable' && record.metadata.namespace === rack.metadata.namespace && record.spec.lanes?.some(lane => [lane.a, lane.b].some(end => end.deviceRef && names.has(end.deviceRef.name))));
}

function rackFeeds(rack: PhysicalRecord, inventory: Inventory) {
  if (inventory.issues?.PowerFeed) return undefined;
  return inventory.physical.filter(record => record.kind === 'PowerFeed' && record.metadata.namespace === rack.metadata.namespace && record.spec.rackRef?.name === rack.metadata.name);
}

function rackDevices(slots: EquipmentPlacement[]) {
  const seen = new Map<string, Device>();
  for (const slot of slots) for (const device of slot.devices) seen.set(device.metadata.name, device);
  return [...seen.values()].sort((a, b) => displayLabel(a).localeCompare(displayLabel(b)));
}

function Glyph({ state }: { state: LinkState }) {
  return <span aria-hidden="true" className="inventory-connection-glyph">{linkStateGlyph[state]}</span>;
}

function StateSummary({ device, inventory }: { device: Device; inventory: Inventory }) {
  const connections = deviceConnections(device, inventory);
  if (connections.cableAccess === 'denied') return <p className="inventory-muted">Cable details unavailable. Port connections are unknown.</p>;
  const counts = stateCounts(connections.links);
  const states: LinkState[] = ['cabled', 'none', 'conflict', 'unavailable', 'catalog'];
  return <ul className="inventory-rack-panel-states" aria-label="Ports and cables">
    {states.filter(state => counts[state]).map(state => <li key={state} data-state={state}><Glyph state={state} /> {counts[state]} {linkStateWords[state].toLowerCase()}</li>)}
    {!connections.links.length && <li>No components recorded</li>}
  </ul>;
}

function SelectedPanel({ slot, rack, inventory, phone, onClose }: { slot?: EquipmentPlacement; rack: PhysicalRecord; inventory: Inventory; phone: boolean; onClose: () => void }) {
  const titleId = useId();
  const panel = useRef<HTMLElement>(null);
  useEffect(() => { if (phone) panel.current?.querySelector<HTMLElement>('[data-panel-close]')?.focus(); }, [phone, slot?.key]);
  const devicesDenied = !!inventory.issues?.Device;
  const device = slot && !devicesDenied && slot.devices.length === 1 ? slot.devices[0] : undefined;
  const claim = slot?.claim ?? (slot?.kind === 'DeviceClaim' ? slot.record as DeviceClaim : undefined);
  const title = slot ? (device ? displayLabel(device) : slotName(slot)) : 'Selection unavailable';
  return <section ref={panel} className="inventory-rack-panel" data-variant={phone ? 'sheet' : 'side'} role={phone ? 'dialog' : 'region'} aria-modal={phone ? false : undefined} aria-labelledby={titleId} data-testid={phone ? 'rack-device-sheet' : 'rack-device-panel'}>
    <div className="inventory-rack-panel-heading">
      <h3 id={titleId}>{title}</h3>
      <Button data-panel-close="" type="secondary" theme="outline" size="small" htmlType="button" onClick={onClose} aria-label="Close device panel">Close</Button>
    </div>
    {!slot ? <p>This selection is not among the readable records for this rack. It may have moved, been removed, or be unavailable to you.</p> : <>
      <p className="inventory-muted">{slot.basis === 'Claim-derived fallback' ? 'Claim-derived fallback · from the installation plan, not a placement report' : 'Reported placement · not verified'}</p>
      {slot.multipleRegistrations && <p className="inventory-note">Multiple registrations · the active device is unknown.</p>}
      {device ? <DeviceFaceplate device={device} inventory={inventory} /> : devicesDenied ? <p>Device details unavailable.</p> : slot.devices.length > 1 ? <ul>{slot.devices.map(item => <li key={item.metadata.name}><Link to={inventoryHref('Device', item.metadata.namespace, item.metadata.name)}>{displayLabel(item)}</Link></li>)}</ul> : <p className="inventory-muted">No device registration recorded for this installation plan.</p>}
      <dl className="inventory-rack-panel-fields">
        <div><dt>Model</dt><dd>{device ? deviceModelLabel(device, inventory) : claim ? `${modelLabel(claim.spec.expectedIdentity)} (expected)` : 'Not recorded'}</dd></div>
        <div><dt>Units</dt><dd>{unitsLabel(slot, rack)}</dd></div>
        <div><dt>Rack face</dt><dd>{slot.placement.face ?? 'Not recorded'}</dd></div>
        <div><dt>Depth</dt><dd>{depthLabel(equipmentDepth(slot, inventory))}</dd></div>
        <div><dt>Lifecycle</dt><dd>{device ? device.spec.lifecycle ?? 'Active' : devicesDenied ? 'Unavailable' : 'No device'}</dd></div>
      </dl>
      {device && <StateSummary device={device} inventory={inventory} />}
      <p className="inventory-rack-panel-links">
        {device && <Link to={inventoryHref('Device', device.metadata.namespace, device.metadata.name)}>Open device page</Link>}
        {claim && !inventory.issues?.DeviceClaim && <Link to={inventoryHref('DeviceClaim', claim.metadata.namespace, claim.metadata.name)}>Installation plan</Link>}
      </p>
    </>}
  </section>;
}

function EquipmentTable({ slots, rack, inventory, label }: { slots: EquipmentPlacement[]; rack: PhysicalRecord; inventory: Inventory; label: string }) {
  const devicesDenied = !!inventory.issues?.Device;
  return <div className="inventory-scroll"><Table aria-label={label}>
    <TableHeader><TableRow><TableHead>Device</TableHead><TableHead>U / face / depth</TableHead><TableHead>Lifecycle</TableHead><TableHead>Claim / registration</TableHead></TableRow></TableHeader>
    <TableBody>{slots.map(slot => <TableRow key={slot.key}>
      <TableCell>
        {devicesDenied ? 'Device details unavailable' : slot.devices.length ? slot.devices.map(device => <div key={device.metadata.name}><Link to={inventoryHref('Device', device.metadata.namespace, device.metadata.name)}>{displayLabel(device)}</Link></div>) : 'No device registration recorded'}
        {slot.devices.length > 1 && <small>Multiple registrations · active device unknown</small>}
      </TableCell>
      <TableCell>
        {unitsLabel(slot, rack)}
        {slot.placement.scope !== 'NonRackUnitMount' && <small>{slot.placement.face ?? 'Face not recorded'} · {depthLabel(equipmentDepth(slot, inventory)).toLowerCase()}</small>}
        {slot.basis === 'Claim-derived fallback' && <small>Claim-derived fallback</small>}
      </TableCell>
      <TableCell>{devicesDenied ? 'Unavailable' : slot.devices.length ? slot.devices.map(device => device.spec.lifecycle ?? 'Active').join(', ') : 'No device'}</TableCell>
      <TableCell>{slot.claim ? <Link to={inventoryHref('DeviceClaim', slot.claim.metadata.namespace, slot.claim.metadata.name)}>{displayLabel(slot.claim)}</Link> : inventory.issues?.DeviceClaim ? 'Claim details unavailable' : 'No claim recorded'}</TableCell>
    </TableRow>)}</TableBody>
  </Table></div>;
}

function EquipmentTab({ rack, slots, inventory }: { rack: PhysicalRecord; slots: EquipmentPlacement[]; inventory: Inventory }) {
  const denied = !!inventory.issues?.DeviceClaim && !slots.length;
  const positioned = slots.filter(slot => equipmentGeometry(slot, rack)).sort((a, b) => (b.placement.position ?? 0) - (a.placement.position ?? 0));
  const nonRacked = slots.filter(slot => slot.placement.scope === 'NonRackUnitMount');
  const unpositioned = slots.filter(slot => !equipmentGeometry(slot, rack) && slot.placement.scope !== 'NonRackUnitMount');
  return <div className="inventory-equipment-sections">
    <p className="inventory-muted">Reported placements, not verified occupancy.</p>
    {(inventory.issues?.Device || inventory.issues?.DeviceClaim) && <p>Some records are unavailable; equipment counts are incomplete.</p>}
    {denied ? <p>Installation details unavailable. This is not an empty rack.</p> : <>
      <section><h4>Positioned equipment <span className="inventory-count">{positioned.length}</span></h4>{positioned.length ? <EquipmentTable slots={positioned} rack={rack} inventory={inventory} label="Positioned equipment" /> : <p className="inventory-muted">No complete in-range unit positions recorded.</p>}</section>
      <section id="rack-unpositioned"><h4>Unpositioned equipment <span className="inventory-count">{unpositioned.length}</span></h4>{unpositioned.length ? <EquipmentTable slots={unpositioned} rack={rack} inventory={inventory} label="Unpositioned equipment" /> : <p className="inventory-muted">No unpositioned reports or claims recorded for this rack.</p>}</section>
      <section><h4>Non-racked mounts <span className="inventory-count">{nonRacked.length}</span></h4>{nonRacked.length ? <EquipmentTable slots={nonRacked} rack={rack} inventory={inventory} label="Non-racked mounts" /> : <p className="inventory-muted">No side, rail, or other non-unit mounts recorded.</p>}</section>
    </>}
  </div>;
}

function CablesTab({ slots, inventory }: { slots: EquipmentPlacement[]; inventory: Inventory }) {
  if (inventory.issues?.Cable) return <p>Cable details unavailable. Connections for this rack are unknown, not absent.</p>;
  if (inventory.issues?.Device) return <p>Device details unavailable. Cables cannot be grouped by device.</p>;
  const groups = rackDevices(slots).map(device => {
    const links = deviceConnections(device, inventory).links.filter(link => link.cable);
    return { device, links, cables: new Map(links.map(link => [link.cable!.name, link.cable!.label])) };
  });
  return <div className="inventory-equipment-sections">
    <p className="inventory-muted">Recorded cables, not live links. No cable recorded means unknown, not free.</p>
    {groups.map(({ device, links, cables }) => <section key={device.metadata.name} aria-label={`${displayLabel(device)} cables`}>
      <h4><Link to={inventoryHref('Device', device.metadata.namespace, device.metadata.name)}>{displayLabel(device)}</Link> <span className="inventory-count">{cables.size} {cables.size === 1 ? 'cable' : 'cables'}</span></h4>
      {links.length ? <div className="inventory-scroll"><Table aria-label={`${displayLabel(device)} cables`}>
        <TableHeader><TableRow><TableHead>Port</TableHead><TableHead>Peer</TableHead><TableHead>Status</TableHead><TableHead>Cable</TableHead></TableRow></TableHeader>
        <TableBody>{links.map(link => <TableRow key={link.key}>
          <TableCell>{localLabel(link)}</TableCell>
          <TableCell>{peerLabel(link.peer)}</TableCell>
          <TableCell data-state={link.state}><Glyph state={link.state} /> {linkStateWords[link.state]}</TableCell>
          <TableCell><Link to={inventoryHref('Cable', device.metadata.namespace, link.cable!.name)}>{link.cable!.label}</Link>{link.cable!.laneCount > 1 && <small>Lane {link.cable!.laneID} of {link.cable!.laneCount}</small>}</TableCell>
        </TableRow>)}</TableBody>
      </Table></div> : <p className="inventory-muted">No cable records name this device.</p>}
      {cables.size > 0 && <details className="inventory-record-names"><summary>Record names</summary><ul>{[...cables].map(([name, label]) => <li key={name}>{label} · <code>{name}</code></li>)}</ul></details>}
    </section>)}
    {!groups.length && <p className="inventory-muted">No devices are recorded in this rack, so no cables are grouped here.</p>}
  </div>;
}

function feedUse(feed: PhysicalRecord, inventory: Inventory) {
  const uses = new Map<string, string[]>();
  for (const cable of inventory.physical) {
    if (cable.kind !== 'Cable' || cable.metadata.namespace !== feed.metadata.namespace) continue;
    for (const lane of cable.spec.lanes ?? []) for (const [mine, far] of [[lane.a, lane.b], [lane.b, lane.a]] as const) {
      if (mine.powerFeedRef?.name !== feed.metadata.name) continue;
      const device = far.deviceRef && !inventory.issues?.Device ? [...inventory.slots.flatMap(slot => slot.devices), ...inventory.unboundDevices].find(item => item.metadata.namespace === feed.metadata.namespace && item.metadata.name === far.deviceRef!.name) : undefined;
      const peer = far.deviceRef ? inventory.issues?.Device ? 'Device unavailable' : device ? `${displayLabel(device)} · ${device.spec.components?.find(item => item.id === far.componentID)?.name ?? far.componentID}` : `${far.deviceRef.name} (record not found)` : 'Other power feed';
      uses.set(`${mine.componentID}/${mine.position}`, [...(uses.get(`${mine.componentID}/${mine.position}`) ?? []), `${peer} via ${displayLabel(cable)}`]);
    }
  }
  return uses;
}

function PowerTab({ rack, slots, inventory }: { rack: PhysicalRecord; slots: EquipmentPlacement[]; inventory: Inventory }) {
  const feeds = rackFeeds(rack, inventory);
  const cablesDenied = !!inventory.issues?.Cable;
  const pdus = inventory.issues?.Device ? undefined : rackDevices(slots).filter(device => device.spec.components?.some(component => component.type === 'PowerOutlet'));
  const devices = inventory.issues?.Device ? undefined : rackDevices(slots);
  return <div className="inventory-equipment-sections">
    <p className="inventory-muted">Ratings, recorded cables and observed load; available power is unknown. No cable recorded means unknown, not free.</p>
    {!feeds ? null : <RackPowerRedundancy rack={rack} feeds={feeds} devices={devices} inventory={inventory} />}
    <section aria-label="Power feeds"><h4>Feed outlets <span className="inventory-count">{feeds ? feeds.length : 'unavailable'}</span></h4>
      {!feeds ? <p>Power feed details unavailable.</p> : feeds.length ? <ul className="inventory-power-list">{feeds.map(feed => {
        const uses = cablesDenied ? undefined : feedUse(feed, inventory);
        const supply = feed.spec.supply;
        return <li key={feed.metadata.name}>
          <p><strong>{displayLabel(feed)}</strong> <small>{supply ? `${supply.type} · ${supply.voltageV ?? 'Unknown'} V · ${supply.currentA ?? 'Unknown'} A${supply.phase ? ` · ${supply.phase} phase` : ''}` : 'Supply not recorded'}</small></p>
          {!uses ? <p>Cable details unavailable. Outlet use is unknown.</p> : <ul aria-label={`${displayLabel(feed)} outlets`}>{(feed.spec.terminations ?? []).flatMap(termination => Array.from({ length: termination.positions ?? 1 }, (_, index) => {
            const key = `${termination.id}/${index + 1}`;
            const used = uses.get(key);
            return <li key={key} data-state={used ? used.length > 1 ? 'conflict' : 'cabled' : 'none'}><Glyph state={used ? used.length > 1 ? 'conflict' : 'cabled' : 'none'} /> {termination.name}{(termination.positions ?? 1) > 1 ? ` · position ${index + 1}` : ''}: {used ? used.join('; ') : 'no cable recorded'}</li>;
          }))}</ul>}
        </li>;
      })}</ul> : <p className="inventory-muted">No power feeds name this rack.</p>}
    </section>
    <section aria-label="PDUs"><h4>PDUs <span className="inventory-count">{pdus ? pdus.length : 'unavailable'}</span></h4>
      {!pdus ? <p>Device details unavailable.</p> : pdus.length ? <ul className="inventory-power-list">{pdus.map(pdu => {
        const connections = deviceConnections(pdu, inventory);
        const outlets = connections.links.filter(link => link.component?.type === 'PowerOutlet' || link.template?.type === 'PowerOutlet');
        const used = outlets.filter(link => link.cable);
        const outletCount = (pdu.spec.components ?? []).filter(component => component.type === 'PowerOutlet').reduce((sum, component) => sum + (component.positions ?? 1), 0);
        return <li key={pdu.metadata.name}>
          <p><Link to={inventoryHref('Device', pdu.metadata.namespace, pdu.metadata.name)}>{displayLabel(pdu)}</Link> <small>{connections.cableAccess === 'denied' ? 'Cable details unavailable. Outlet use is unknown.' : `${new Set(used.map(link => `${link.componentID}/${link.position}`)).size} of ${outletCount} outlets have a cable recorded`}</small></p>
          {connections.cableAccess !== 'denied' && used.length > 0 && <ul aria-label={`${displayLabel(pdu)} outlets in use`}>{used.map(link => <li key={link.key} data-state={link.state}><Glyph state={link.state} /> {localLabel(link)} → {peerLabel(link.peer)}{link.state !== 'cabled' ? ` · ${linkStateWords[link.state]}` : ''}</li>)}</ul>}
        </li>;
      })}</ul> : <p className="inventory-muted">No device in this rack records power outlets.</p>}
    </section>
  </div>;
}

function HistoryTab({ rack }: { rack: PhysicalRecord }) {
  return <section aria-labelledby="rack-history-title" className="inventory-equipment-sections">
    <Title as="h3" level={5} id="rack-history-title">Record history & verification</Title>
    <dl className="inventory-fields">
      <div><dt>Verification</dt><dd>Unknown — no verification evidence is available.</dd></div>
      <div><dt>Created</dt><dd>{rack.metadata.creationTimestamp ?? 'Not recorded'}</dd></div>
    </dl>
  </section>;
}

export function RackDetail({ rack, inventory, management }: { rack: PhysicalRecord; inventory: Inventory; management?: ReactNode }) {
  const [params, setParams] = useSearchParams();
  const phone = usePhone();
  const navigate = useNavigate();
  const stage = useRef<HTMLDivElement>(null);
  const returnFocus = useRef<string | undefined>(undefined);
  const requested = params.get(RACK_PARAMS.tab);
  const tab: RackTab = RACK_TABS.find(item => item.tab === requested)?.tab ?? 'elevation';
  const slots = rackPlacements(rack, inventory);
  const selectedKind = params.get(RACK_PARAMS.selectedKind);
  const selectedName = params.get(RACK_PARAMS.selected);
  const selecting = tab === 'elevation' && (selectedKind === 'Device' || selectedKind === 'DeviceClaim') && !!selectedName;
  const selectedKey = selecting ? `${selectedKind}/${slotKey(rack.metadata.namespace, selectedName!)}` : undefined;
  const selected = selectedKey ? slots.find(slot => slot.key === selectedKey) : undefined;
  const cables = rackCables(rack, slots, inventory);
  const feeds = rackFeeds(rack, inventory);
  const issues = rackIssues(rack, slots, inventory);
  const equipmentDenied = !!inventory.issues?.DeviceClaim && !slots.length;
  const incomplete = !!inventory.issues?.Device || !!inventory.issues?.DeviceClaim;

  const query = (change: (next: URLSearchParams) => void) => { const next = new URLSearchParams(params); change(next); return `?${next}`; };
  const tabHref = (value: RackTab) => query(next => { next.delete(RACK_PARAMS.selected); next.delete(RACK_PARAMS.selectedKind); if (value === 'elevation') next.delete(RACK_PARAMS.tab); else next.set(RACK_PARAMS.tab, value); });
  const selectHref = (slot: EquipmentPlacement) => query(next => { next.delete(RACK_PARAMS.tab); next.set(RACK_PARAMS.selectedKind, slot.kind); next.set(RACK_PARAMS.selected, slot.record.metadata.name); });
  const closeSelection = () => {
    returnFocus.current = selectedKey;
    setParams(previous => { const next = new URLSearchParams(previous); next.delete(RACK_PARAMS.selected); next.delete(RACK_PARAMS.selectedKind); return next; });
  };
  useEffect(() => {
    if (selecting || !returnFocus.current) return;
    stage.current?.querySelector<HTMLElement>(`a[data-slot-key="${CSS.escape(returnFocus.current)}"]`)?.focus();
    returnFocus.current = undefined;
  }, [selecting]);

  const onStageKey = (event: KeyboardEvent<HTMLDivElement>) => {
    const target = event.target as HTMLElement;
    if (event.key === 'Escape' && selecting && !target.closest('[data-component-card]')) { event.preventDefault(); closeSelection(); return; }
    if (!target.matches('a[data-slot-key]')) return;
    // Enter selects: the link's own activation, made explicit so it never depends on the browser.
    if (event.key === 'Enter' && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) { const slot = slots.find(item => item.key === target.dataset.slotKey); if (slot) { event.preventDefault(); void navigate(selectHref(slot)); } return; }
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const items = [...(stage.current?.querySelectorAll<HTMLElement>('.inventory-rack-faces a[data-slot-key]') ?? [])];
    const index = items.indexOf(target);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : index + (event.key === 'ArrowDown' ? 1 : -1);
    if (next < 0 || next >= items.length) return;
    event.preventDefault();
    items[next].focus();
  };

  const count = (value: number | undefined, partial = false) => value === undefined ? 'unavailable' : `${value}${partial ? '+' : ''}`;
  const counts: { tab: RackTab; label: string; value: string }[] = [
    { tab: 'equipment', label: 'Equipment', value: equipmentDenied ? 'unavailable' : count(slots.length, incomplete) },
    { tab: 'cables', label: 'Cables', value: count(cables?.length) },
    { tab: 'power', label: 'Power feeds', value: count(feeds?.length) },
  ];
  const nothingRelated = !slots.length && cables?.length === 0 && feeds?.length === 0 && !incomplete;
  const facility = rack.spec.facilityRef?.name;
  const facilityRecord = facility && !inventory.issues?.Facility ? inventory.physical.find(record => record.kind === 'Facility' && record.metadata.namespace === rack.metadata.namespace && record.metadata.name === facility) : undefined;

  const overlaps = issues.filter(issue => issue.kind === 'overlap').reduce((sum, issue) => sum + issue.slots.length, 0);
  const unpositioned = issues.find(issue => issue.kind === 'unpositioned')?.slots.length ?? 0;
  const elevationHref = tabHref('elevation');

  return <div className="inventory-rack-page">
    <div className="inventory-rack-header">
      <p className="inventory-rack-summary">
        <span>{!facility ? 'Facility not recorded' : inventory.issues?.Facility ? 'Facility unavailable' : facilityRecord ? <Link to={inventoryHref('Facility', rack.metadata.namespace, facility)}>{displayLabel(facilityRecord)}</Link> : 'Facility record not found'}</span>
        <span>{rack.spec.heightUnits === undefined ? 'Height not recorded' : `${rack.spec.heightUnits}U`}</span>
        {overlaps > 0 && <Link className="inventory-issue-badge" data-kind="overlap" to={elevationHref}><span aria-hidden="true">! </span>{overlaps} placements disagree</Link>}
        {unpositioned > 0 && <Link className="inventory-issue-badge" data-kind="unpositioned" to={elevationHref}><span aria-hidden="true">? </span>{unpositioned} without a position</Link>}
      </p>
      <nav className="inventory-rack-counts" aria-label="Related records">
        <ul>{counts.map(item => <li key={item.tab}><Link to={tabHref(item.tab)} aria-current={tab === item.tab ? 'true' : undefined}>{item.label} · {item.value}</Link></li>)}</ul>
      </nav>
      <p className="inventory-rack-free">Free space: Unknown, recorded placement is not verified occupancy.</p>
      {nothingRelated && <p className="inventory-muted">No related records found in this namespace. This does not establish that equipment or connections are absent.</p>}
    </div>
    <Tabs value={tab} activationMode="manual" className="inventory-tab-row inventory-rack-tabs">
      <TabsList variant="line" aria-label="Rack views">
        {RACK_TABS.map(item => <TabsLinkTrigger key={item.tab} value={item.tab} href={tabHref(item.tab)} linkComponent={Link} aria-current={item.tab === tab ? 'page' : undefined} aria-controls={undefined}
          onKeyDown={event => { if (event.key === ' ') { event.preventDefault(); event.currentTarget.click(); } }}>{item.title}</TabsLinkTrigger>)}
      </TabsList>
    </Tabs>
    <section className="inventory-rack-tab" aria-label={RACK_TABS.find(item => item.tab === tab)!.title} data-tab={tab}>
      {tab === 'elevation' && <>
        {issues.length > 0 && <div className="inventory-rack-issues" role="region" aria-label="Rack issues">
          {issues.map(issue => <p key={`${issue.kind}/${issue.slots.map(slot => slot.key).join('|')}`} className="inventory-banner" data-kind={issue.kind}>
            <span aria-hidden="true" className="inventory-banner-glyph">!</span>
            <span>{issue.text} {issue.slots.map((slot, index) => <span key={slot.key}>{index > 0 && ', '}<Link to={selectHref(slot)}>{slotName(slot)}</Link>{slot.basis === 'Claim-derived fallback' && ' (claim-derived fallback)'}</span>)}</span>
          </p>)}
        </div>}
        <div ref={stage} className="inventory-rack-stage" data-view="2d" data-selected={selecting ? '' : undefined} onKeyDown={onStageKey}>
          <RackOverview rack={rack} inventory={inventory} selection={{ href: selectHref, selectedKey }} />
          {selecting ? <SelectedPanel key={selectedKey} slot={selected} rack={rack} inventory={inventory} phone={phone} onClose={closeSelection} /> : !phone && <aside className="inventory-rack-panel inventory-rack-panel-empty" aria-label="Device panel">
            <p className="inventory-muted">Select equipment to see its faceplate. Arrow keys move, Enter selects, Escape closes.</p>
          </aside>}
        </div>
      </>}
      {tab === 'equipment' && <EquipmentTab rack={rack} slots={slots} inventory={inventory} />}
      {tab === 'cables' && <CablesTab slots={slots} inventory={inventory} />}
      {tab === 'power' && <PowerTab rack={rack} slots={slots} inventory={inventory} />}
      {tab === 'history' && <HistoryTab rack={rack} />}
    </section>
    <details className="inventory-rack-about">
      <summary>About this record</summary>
      <dl className="inventory-rack-meta">
        <div><dt>Record name</dt><dd><code>{rack.metadata.name}</code></dd></div>
        <div><dt>Namespace</dt><dd>{rack.metadata.namespace || 'Not recorded'}</dd></div>
        {rack.metadata.uid && <div><dt>UID</dt><dd><code>{rack.metadata.uid}</code></dd></div>}
        {facility && facilityRecord && <div><dt>Facility record</dt><dd><code>{facility}</code></dd></div>}
      </dl>
      {management && <div className="inventory-rack-manage">{management}</div>}
    </details>
  </div>;
}
