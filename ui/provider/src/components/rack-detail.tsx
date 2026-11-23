// The Rack page: the elevation is the main content, with a selected device's
// faceplate in a side panel (a bottom sheet on phones), and tabs for the
// equipment table, cables and power, then the Record section. The tab and the
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
import { deviceConnections, linkStateGlyph, linkStateWords, localLabel, peerLabel, portTiles, type LinkState } from '../lib/device-connections';
import { classifyFeeds, FEED_CLASS_WORDS, psuFeeds } from '../lib/power-paths';
import { equipmentGeometry, type EquipmentPlacement } from '../lib/device-placement';
import { displayLabel } from '../lib/display-names';
import { depthLabel, equipmentDepth } from '../lib/device-depth';
import { faceOccupancy, RACK_FACES, rackOccupants, type RackOccupant } from '../lib/rack-faces';
import { deviceModelLabel } from '../lib/hardware-catalog';
import { DeviceFaceplate } from './faceplate-drawing';
import { inventoryHref, rackPlacements, RackOverview } from './inventory-location';
import { RackPowerRedundancy } from './rack-power';
import { RecordFooter } from './inventory-record-layout';

export const RACK_TABS = [
  { tab: 'elevation', title: 'Elevation' },
  { tab: 'equipment', title: 'Equipment' },
  { tab: 'cables', title: 'Cables' },
  { tab: 'power', title: 'Power' },
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
      {/* One line of facts; lifecycle only when it is not the normal state. */}
      <p className="inventory-rack-panel-facts">
        {[
          device ? deviceModelLabel(device, inventory) : claim ? `${modelLabel(claim.spec.expectedIdentity)} (expected)` : undefined,
          unitsLabel(slot, rack),
          slot.placement.face,
          depthLabel(equipmentDepth(slot, inventory)).toLowerCase(),
          device && device.spec.lifecycle && device.spec.lifecycle !== 'Active' ? device.spec.lifecycle : undefined,
          slot.basis === 'Claim-derived fallback' ? 'planned, no placement report' : undefined,
        ].filter(Boolean).join(' · ')}
      </p>
      {slot.multipleRegistrations && <p className="inventory-note">Multiple registrations · the active device is unknown.</p>}
      {device ? <DeviceFaceplate device={device} inventory={inventory} /> : devicesDenied ? <p>Device details unavailable.</p> : slot.devices.length > 1 ? <ul>{slot.devices.map(item => <li key={item.metadata.name}><Link to={inventoryHref('Device', item.metadata.namespace, item.metadata.name)}>{displayLabel(item)}</Link></li>)}</ul> : <p className="inventory-muted">No device registration recorded for this installation plan.</p>}
      <p className="inventory-rack-panel-links">
        {device && <Link to={inventoryHref('Device', device.metadata.namespace, device.metadata.name)}>Open device page</Link>}
        {claim && !inventory.issues?.DeviceClaim && <Link to={inventoryHref('DeviceClaim', claim.metadata.namespace, claim.metadata.name)}>Installation plan</Link>}
      </p>
    </>}
  </section>;
}

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

/**
 * Every recorded placement in this rack in one dense table, top of rack
 * first: position, device, model, face, depth, cabled ports, the feeds its
 * PSUs reach, and serial and asset tag when any row records them. A row that
 * needs checking carries the elevation's colour edge.
 */
function EquipmentTable({ slots, rack, inventory }: { slots: EquipmentPlacement[]; rack: PhysicalRecord; inventory: Inventory }) {
  const devicesDenied = !!inventory.issues?.Device;
  const rows = [...slots].sort((a, b) => rowOrder(b, rack) - rowOrder(a, rack)).map(slot => {
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
  return <div className="inventory-scroll"><Table aria-label="Rack equipment" className="inventory-dense-table">
    <TableHeader><TableRow>
      <TableHead>U</TableHead><TableHead>Device</TableHead><TableHead>Model</TableHead><TableHead>Face</TableHead><TableHead>Depth</TableHead><TableHead>Ports</TableHead><TableHead>Power</TableHead>
      {serials && <TableHead>Serial</TableHead>}
      {tags && <TableHead>Asset tag</TableHead>}
    </TableRow></TableHeader>
    <TableBody>{rows.map(({ slot, device, ports, power, depth }) => <TableRow key={slot.key} data-attention={ports?.attention}>
      <TableCell className="inventory-numeric">{positionLabel(slot, rack)}</TableCell>
      <TableCell>
        {devicesDenied ? 'Unavailable' : slot.devices.length ? slot.devices.map(item => <Link key={item.metadata.name} to={inventoryHref('Device', item.metadata.namespace, item.metadata.name)}>{displayLabel(item)}</Link>) : <span className="inventory-muted" title="Installation plan with no device registered">{slotName(slot)}</span>}
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
    </TableRow>)}</TableBody>
  </Table></div>;
}

function EquipmentTab({ rack, slots, inventory }: { rack: PhysicalRecord; slots: EquipmentPlacement[]; inventory: Inventory }) {
  const denied = !!inventory.issues?.DeviceClaim && !slots.length;
  return <div className="inventory-equipment-sections">
    {(inventory.issues?.Device || inventory.issues?.DeviceClaim) && <p>Some records are unavailable; equipment counts are incomplete.</p>}
    {denied ? <p>Installation details unavailable. This is not an empty rack.</p> : slots.length ? <EquipmentTable slots={slots} rack={rack} inventory={inventory} /> : <p className="inventory-muted">No equipment recorded in this rack.</p>}
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

/**
 * The redundancy grid is the whole tab. Each feed's column carries its
 * outlets as tiles and each PDU's row its outlet count, so the per-feed and
 * per-PDU lists this tab used to repeat below the grid are not needed; every
 * outlet's peer and Cable is in its tile's name, tooltip and card, and on
 * the feed's power path.
 */
function PowerTab({ rack, slots, inventory }: { rack: PhysicalRecord; slots: EquipmentPlacement[]; inventory: Inventory }) {
  const feeds = rackFeeds(rack, inventory);
  const devices = inventory.issues?.Device ? undefined : rackDevices(slots);
  return <div className="inventory-equipment-sections">
    {!feeds ? <p>Power feed details unavailable.</p> : !feeds.length && <p className="inventory-muted">No power feeds name this rack.</p>}
    {/* With feeds denied the grid still lists each device and PDU; every PSU's feed reads unknown and no feed is named. */}
    <RackPowerRedundancy rack={rack} feeds={feeds ?? []} devices={devices} inventory={inventory} />
  </div>;
}

export function RackDetail({ rack, inventory }: { rack: PhysicalRecord; inventory: Inventory }) {
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
    // Entries are equipment links, in visual order per face.
    if (!target.matches('a[data-slot-key]')) return;
    // Enter selects: the link's own activation, made explicit so it never depends on the browser.
    if (event.key === 'Enter' && target.matches('a[data-slot-key]') && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) { const slot = slots.find(item => item.key === target.dataset.slotKey); if (slot) { event.preventDefault(); void navigate(selectHref(slot)); } return; }
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const items = [...(stage.current?.querySelectorAll<HTMLElement>('.inventory-rack-faces a[data-slot-key]') ?? [])];
    const index = items.indexOf(target);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : index + (event.key === 'ArrowDown' ? 1 : -1);
    if (next < 0 || next >= items.length) return;
    event.preventDefault();
    items[next].focus();
  };

  // Each tab carries its count; "?" when the collection is unreadable, "+" when it is incomplete.
  const count = (value: number | undefined, partial = false) => value === undefined ? '?' : `${value}${partial ? '+' : ''}`;
  const counts: Partial<Record<RackTab, string>> = {
    equipment: equipmentDenied ? '?' : count(slots.length, incomplete),
    cables: count(cables?.length),
    power: count(feeds?.length),
  };
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
      {nothingRelated && <p className="inventory-muted">No related records found.</p>}
    </div>
    <Tabs value={tab} activationMode="manual" className="inventory-tab-row inventory-rack-tabs">
      <TabsList variant="line" aria-label="Rack views">
        {RACK_TABS.map(item => <TabsLinkTrigger key={item.tab} value={item.tab} href={tabHref(item.tab)} linkComponent={Link} aria-current={item.tab === tab ? 'page' : undefined} aria-controls={undefined}
          onKeyDown={event => { if (event.key === ' ') { event.preventDefault(); event.currentTarget.click(); } }}>{item.title}{counts[item.tab] !== undefined && <span className="inventory-count"> {counts[item.tab]}</span>}</TabsLinkTrigger>)}
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
          {/* With nothing selected the elevation takes the full width; the panel opens beside it on selection. */}
          {selecting && <SelectedPanel key={selectedKey} slot={selected} rack={rack} inventory={inventory} phone={phone} onClose={closeSelection} />}
        </div>
      </>}
      {tab === 'equipment' && <EquipmentTab rack={rack} slots={slots} inventory={inventory} />}
      {tab === 'cables' && <CablesTab slots={slots} inventory={inventory} />}
      {tab === 'power' && <PowerTab rack={rack} slots={slots} inventory={inventory} />}
    </section>
    <RecordFooter metadata={rack.metadata} />
  </div>;
}
