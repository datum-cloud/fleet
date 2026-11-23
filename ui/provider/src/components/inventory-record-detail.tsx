// Per-kind detail pages, following "Page design rules" in
// docs/development/staff-inventory-ui.md. Each kind returns a one-line
// summary (a Device adds one strip of identity facts), issue banners, one
// primary view and secondary tabs. Generated
// identity lives in "About this record" (see inventory-record-layout.tsx).
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from '@datum-cloud/datum-ui/table';
import { useMemo, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { modelLabel, slotKey, type Inventory, type InventorySlot, type RawDevice, type RawDeviceClaim } from '../inventory';
import type { CableEndpoint, PhysicalRecord } from '../../../shared/fleet-api';
import { devicePlacement, placementFacility, type EquipmentPlacement } from '../lib/device-placement';
import { deviceCatalog, deviceModelLabel } from '../lib/hardware-catalog';
import { depthLabel, deviceDepth } from '../lib/device-depth';
import { interfaceAddresses, typeTitle } from '../lib/device-connections';
import { facilityIdentifiers, facilitySites } from '../lib/site-membership';
import { displayLabel, sharedValue } from '../lib/display-names';
import { DeviceConnections } from './device-connections';
import type { ComponentHighlight } from './device-faceplate';
import { FaceplateDrawing, modelDrawingModel } from './faceplate-drawing';
import { DeviceTable, facilityRackCount, FacilityRacks, FacilityUnrackedEquipment, unrackedCount } from './inventory-location';
import { FactSection, Facts, RecordSummary, SmallTable, SummaryParts, type FactItem, type RecordIssue, type RecordTab, type SummaryBadge } from './inventory-record-layout';
import { observedDisagrees, type TelemetryContextValue } from '../lib/telemetry';
import { feedPath } from '../lib/power-paths';
import { DeviceDrawFact, PowerFeedPath } from './power-feed-path';
import { CapacityBar, useTelemetry } from './power-telemetry';

export type DetailKind = 'Site' | 'Device' | 'Facility' | 'Rack' | 'DeviceClaim' | 'Cable' | 'PowerFeed' | 'Manufacturer' | 'DeviceType';
export type DetailEntry = { kind: DetailKind; record: RawDevice | RawDeviceClaim | PhysicalRecord };

/** Lookups the directory already computes; passed in so links stay consistent. */
export interface DetailHelpers {
  inventory: Inventory;
  entries: DetailEntry[];
  href: (kind: string, namespace?: string, name?: string) => string;
  reference: (kind: DetailKind, namespace: string | undefined, name: string) => ReactNode;
  find: (kind: DetailKind, namespace: string | undefined, name: string) => DetailEntry | undefined;
  placement: (entry: DetailEntry) => string;
  slotFor: (entry: DetailEntry) => InventorySlot | undefined;
  /** Observed telemetry; absent means none is configured. */
  telemetry?: TelemetryContextValue;
}

export interface DetailView {
  summary: ReactNode;
  issues: RecordIssue[];
  primary: ReactNode;
  tabs: RecordTab[];
  about?: FactItem[];
}

const recordLabel = displayLabel;

/** Facility › Rack · position, with links, for a placement. */
function PlacementLinks({ placement, namespace, helpers, omitFacility }: { placement: EquipmentPlacement['placement']; namespace?: string; helpers: DetailHelpers; omitFacility?: boolean }) {
  const { inventory, reference } = helpers;
  const facility = placementFacility(placement, namespace, inventory);
  const position = placement.mount ? `Mount: ${placement.mount}`
    : placement.position !== undefined ? `U${placement.position}${(placement.height ?? 1) > 1 ? `–U${placement.position + (placement.height ?? 1) - 1}` : ''}${placement.face ? ` ${placement.face}` : ''}`
      : placement.rackRef ? 'position unknown' : 'rack unknown';
  const parts: ReactNode[] = [
    !omitFacility && (inventory.issues?.Facility ? 'Facility unavailable' : facility ? reference('Facility', namespace, facility) : 'Facility unknown'),
    placement.rackRef && (inventory.issues?.Rack ? 'Rack unavailable' : reference('Rack', namespace, placement.rackRef.name)),
    position,
  ];
  return <SummaryParts parts={parts} />;
}

const lifecycleBadge = (lifecycle?: string): SummaryBadge[] => !lifecycle || lifecycle === 'Active' ? [] : [{ label: lifecycle, tone: lifecycle === 'Retired' ? 'muted' : 'warning' }];

function RecordLinks({ kind, records, helpers, empty }: { kind: DetailKind; records: { metadata: { name: string; namespace?: string }; spec: { displayName?: string } }[]; helpers: DetailHelpers; empty: string }) {
  if (!records.length) return <p className="inventory-muted">{empty}</p>;
  return <ul className="inventory-link-list">{records.map(record => <li key={record.metadata.name}><Link to={helpers.href(kind, record.metadata.namespace, record.metadata.name)}>{recordLabel(record)}</Link></li>)}</ul>;
}

// ---- Device ----

export function deviceDetail(device: RawDevice, helpers: DetailHelpers, locationEditor?: ReactNode): DetailView {
  const { inventory, reference, href } = helpers;
  const namespace = device.metadata.namespace;
  const catalog = deviceCatalog(device, inventory);
  const projected = devicePlacement(device, inventory);
  const slot = helpers.slotFor({ kind: 'Device', record: device });
  const components = device.spec.components ?? [];
  const depth = deviceDepth(device, inventory);
  const report = device.spec.reportedPlacement;

  const issues: RecordIssue[] = [];
  if (device.spec.identity.unidentified) issues.push({ id: 'identity', tone: 'danger', text: `No serial or system UUID recorded · ${device.spec.identity.unidentifiedReason ?? 'reason not recorded'} · cannot back machine capacity` });
  // Connection problems are not banners: the picture, the table's rows and its count line show them.
  if (inventory.issues?.DeviceClaim) issues.push({ id: 'claim', text: 'Claim details unavailable; location may be incomplete.', tab: 'location' });
  else if (!projected) issues.push({ id: 'location', text: slot ? 'Location unknown: no reported placement or planned location.' : 'Location unknown: no reported placement, and the claim record was not found.', tab: 'location' });
  else if (projected.placement.rackRef && projected.placement.position === undefined && !projected.placement.mount) issues.push({ id: 'position', text: 'Position in the rack is unknown.', tab: 'location' });
  if (slot && slot.devices.length > 1) issues.push({ id: 'registrations', text: 'Multiple devices reference this claim. All registrations are retained; the active device and its placement are unknown.', tab: 'location' });
  if (device.spec.deviceTypeRef && !catalog) issues.push({ id: 'catalog', text: 'Catalog unavailable · showing recorded observation.' });

  // The model is stated once, in the header's facts strip. Without a
  // readable catalog it is the recorded observation, so Registration does not
  // repeat it; the longer reason is the tooltip (and the catalog banner).
  // The model link names the manufacturer in its own label, so it is not repeated.
  const model = catalog
    ? reference('DeviceType', namespace, catalog.model.metadata.name)
    : <span title={device.spec.deviceTypeRef ? 'Catalog unavailable · showing recorded observation' : 'Recorded observation, no shared catalog model'}>{deviceModelLabel(device, inventory)}</span>;
  // Header: the location line, then one strip of identity facts. Empty facts are hidden.
  const summary = <>
    <RecordSummary badges={lifecycleBadge(device.spec.lifecycle)}>
      {projected ? <>
        {projected.basis !== 'Reported placement' && <span className="inventory-muted" title="No reported placement; this is the installation plan's location">Planned · </span>}
        <PlacementLinks placement={projected.placement} namespace={namespace} helpers={helpers} />
      </> : <span>Location unknown</span>}
    </RecordSummary>
    <dl className="inventory-fact-strip" aria-label="Identity">
      <div><dt className="inventory-visually-hidden">Model</dt><dd>{model}</dd></div>
      {device.spec.identity.serial && <div><dt>Serial</dt><dd className="inventory-numeric">{device.spec.identity.serial}</dd></div>}
      {device.spec.assetTag && <div><dt>Asset tag</dt><dd className="inventory-numeric">{device.spec.assetTag}</dd></div>}
      {components.some(component => component.type === 'PowerInlet') && <DeviceDrawFact device={device} />}
    </dl>
  </>;

  const shape = catalog?.model.spec.modelShape;
  // The primary view is the device picture and its component table; nothing else competes with them.
  const primary = <div id="device-connections"><DeviceConnections key={slotKey(namespace, device.metadata.name)} device={device} inventory={inventory} href={href} /></div>;

  // The header already names the facility and rack, and depth has its own
  // fact, so the placement fact keeps only units, height and face.
  const where = projected?.placement;
  const placementFact = projected?.basis === 'Reported placement' && where ? [
    'Reported',
    where.mount ? `Mount: ${where.mount}` : where.position === undefined ? 'Position not recorded' : `U${where.position}`,
    where.height === undefined ? 'Height unknown' : `${where.height}U`,
    where.face ?? 'Face unknown',
    projected.multipleRegistrations ? 'multiple registrations · active device unknown' : undefined,
  ].filter(Boolean).join(' · ') : helpers.placement({ kind: 'Device', record: device });

  const unplaced = interfaceAddresses(device).unplaced;
  const tabs: RecordTab[] = [
    {
      id: 'location', label: 'Location', content: <>
        <Facts items={[
          { label: 'Placement', value: placementFact, always: true },
          { label: 'Placement report', value: report && <>Revision {report.revision} · observed {report.observedAt}<small>Reason: {report.reason} · Evidence: {report.reference}</small></> },
          { label: 'Installation plan', value: inventory.issues?.DeviceClaim ? 'Plan details unavailable' : reference('DeviceClaim', namespace, device.spec.claimRef.name), always: true },
          { label: 'Depth for rack conflicts', value: `${depthLabel(depth)} · ${depth.full ? 'occupies both rack faces' : 'occupies its mounted face only'}`, always: true },
          { label: 'Catalog size', value: shape && (shape.heightUnits !== undefined || shape.fullDepth !== undefined) && `${shape.heightUnits ?? 'Unknown '}U · ${shape.fullDepth === undefined ? 'depth not recorded' : shape.fullDepth ? 'full depth' : 'half depth'}` },
          { label: 'Recorded size', value: device.spec.modelShape && `${device.spec.modelShape.heightUnits ?? 'Unknown'}U · ${device.spec.modelShape.fullDepth === undefined ? 'depth not recorded' : device.spec.modelShape.fullDepth ? 'full depth' : 'partial depth'}` },
        ]} />
        {/* The correction editor states this itself; without it, the legend does. */}
        {locationEditor}
      </>,
    },
    {
      id: 'hardware', label: 'Hardware & management', content: <>
        <FactSection title="Hardware">
          <Facts items={[
            { label: 'Reported model', value: catalog && modelLabel(device.spec.identity) },
            { label: 'System UUID', value: device.spec.identity.systemUUID },
            { label: 'MAC addresses without an interface', value: device.spec.unmappedMACAddresses?.join(', ') },
          ]} />
          {/* Interfaces that match a component show their MAC in the component table. */}
          {!!unplaced.length && <SmallTable label="Interfaces not in the component list" head={['Interface', 'MAC address']} rows={unplaced.map(iface => [iface.name, iface.macAddress ?? 'Not recorded'])} />}
        </FactSection>
        {(device.spec.bmc || device.spec.providerHostRef) && <FactSection title="Management">
          <Facts items={[
            { label: 'Management endpoint', value: device.spec.bmc?.endpoint },
            { label: 'Provider host', value: device.spec.providerHostRef && `${device.spec.providerHostRef.system}/${device.spec.providerHostRef.type}/${device.spec.providerHostRef.id}` },
          ]} />
        </FactSection>}
        {!!device.spec.internalMappings?.length && <FactSection title="Internal paths">
          <SmallTable label="Internal paths" head={['From', 'To']} rows={device.spec.internalMappings.map(mapping => [`${mapping.from.componentID}:${mapping.from.position}`, `${mapping.to.componentID}:${mapping.to.position}`])} />
        </FactSection>}
        {!!device.spec.serviceAssociations?.length && <FactSection title="Service references">
          <SmallTable label="Service references" head={['Component', 'Service']} rows={device.spec.serviceAssociations.map(association => [association.componentID, `${association.serviceRef.system}/${association.serviceRef.type}/${association.serviceRef.id}`])} />
        </FactSection>}
      </>,
    },
  ];
  return { summary, issues, primary, tabs };
}

// ---- Cable ----

interface EndTarget { kind: 'Device' | 'PowerFeed'; name: string }
const endTarget = (endpoint: CableEndpoint): EndTarget | undefined => endpoint.deviceRef ? { kind: 'Device', name: endpoint.deviceRef.name } : endpoint.powerFeedRef ? { kind: 'PowerFeed', name: endpoint.powerFeedRef.name } : undefined;

export function cableDetail(cable: PhysicalRecord, helpers: DetailHelpers): DetailView {
  const { inventory, reference, find } = helpers;
  const namespace = cable.metadata.namespace;
  const lanes = cable.spec.lanes ?? [];
  const issues: RecordIssue[] = [];
  const sides = (['a', 'b'] as const).map(side => {
    const targets = lanes.map(lane => endTarget(lane[side]));
    const shared = sharedValue(targets.map(target => target && `${target.kind}/${target.name}`));
    return { side, targets, shared: shared ? targets[0] : undefined };
  });
  const endLabel = (target: EndTarget | undefined) => {
    if (!target) return 'Endpoint not recorded';
    if (inventory.issues?.[target.kind]) return 'Related endpoint unavailable';
    return reference(target.kind, namespace, target.name);
  };
  for (const { side, targets } of sides) {
    const unreadable = targets.some(target => target && inventory.issues?.[target.kind]);
    const missing = [...new Set(targets.filter(target => target && !inventory.issues?.[target.kind] && !find(target.kind, namespace, target.name)).map(target => target!.name))];
    if (unreadable) issues.push({ id: `${side}-denied`, text: `End ${side.toUpperCase()} records are unavailable to you; that end is unknown.` });
    if (missing.length) issues.push({ id: `${side}-missing`, text: `End ${side.toUpperCase()} names ${missing.length === 1 ? 'a record' : 'records'} not found: ${missing.join(', ')}.` });
  }
  const port = (endpoint: CableEndpoint, side: 'a' | 'b') => {
    const repeated = lanes.filter(lane => lane[side].componentID === endpoint.componentID && slotKey(undefined, endTarget(lane[side])?.name ?? '') === slotKey(undefined, endTarget(endpoint)?.name ?? '')).length > 1;
    return repeated || endpoint.position !== 1 ? `${endpoint.componentID} · position ${endpoint.position}` : endpoint.componentID;
  };
  const plans = inventory.issues?.DeviceClaim ? [] : inventory.slots.map(slot => slot.claim).filter(claim => claim.metadata.namespace === namespace && claim.spec.cableRefs?.some(ref => ref.name === cable.metadata.name));
  const primary = <section className="inventory-cable-ends" aria-label="Cable ends">
    <div className="inventory-cable-grid" role="table" aria-label="Cable lanes">
      <div role="row" className="inventory-cable-head">
        <span role="columnheader">Lane</span>
        {sides.map(({ side, shared }) => <span role="columnheader" key={side}>End {side.toUpperCase()}{shared && <strong>{endLabel(shared)}</strong>}</span>)}
      </div>
      {lanes.map(lane => <div role="row" key={lane.id}>
        <span role="cell">{lane.id}</span>
        {sides.map(({ side, shared }) => <span role="cell" key={side}>{!shared && <strong>{endLabel(endTarget(lane[side]))}</strong>}<span>{port(lane[side], side)}</span></span>)}
      </div>)}
    </div>
    {!lanes.length && <p>No lanes recorded.</p>}
  </section>;
  const summary = <RecordSummary><SummaryParts parts={[cable.spec.medium ?? 'Medium not recorded', `${lanes.length} ${lanes.length === 1 ? 'lane' : 'lanes'}`]} /></RecordSummary>;
  const tabs: RecordTab[] = [];
  if (plans.length) tabs.push({ id: 'plans', label: 'Installation plans', count: plans.length, content: <RecordLinks kind="DeviceClaim" records={plans} helpers={helpers} empty="" /> });
  return { summary, issues, primary, tabs };
}

// ---- PowerFeed ----

export function powerFeedDetail(feed: PhysicalRecord, helpers: DetailHelpers): DetailView {
  const { inventory, reference, href, telemetry } = helpers;
  const namespace = feed.metadata.namespace;
  const path = feedPath(feed, inventory);
  const issues: RecordIssue[] = [];
  if (inventory.issues?.Cable) issues.push({ id: 'cables', text: 'Cable records are unavailable to you.' });
  const conflicted = path.terminations.filter(tile => tile.state === 'conflict');
  if (conflicted.length) issues.push({ id: 'conflict', tone: 'danger', text: `Records disagree: more than one Cable names ${conflicted.map(tile => tile.name).join(', ')}.` });
  // Observed load where no Cable is recorded. Records are not changed; the banner links to them.
  if (telemetry && path.cableAccess === 'readable') {
    const { source, now } = telemetry;
    const feedLabel = recordLabel(feed);
    const seen = [
      ...path.terminations.filter(tile => observedDisagrees(tile.state, source.read({ kind: 'PowerFeed', namespace, name: feed.metadata.name, componentID: tile.componentID }), now))
        .map(tile => ({ key: `feed/${tile.key}`, what: tile.name, holder: feedLabel, link: href('PowerFeed', namespace, feed.metadata.name), q: feed.metadata.name })),
      ...path.pdus.flatMap(pdu => pdu.tiles.filter(tile => observedDisagrees(tile.state, source.read({ kind: 'Device', namespace, name: pdu.device.metadata.name, componentID: tile.componentID }), now))
        .map(tile => ({ key: `${pdu.device.metadata.name}/${tile.key}`, what: tile.name, holder: pdu.label, link: href('Device', namespace, pdu.device.metadata.name), q: pdu.device.metadata.name }))),
    ];
    if (seen.length) issues.push({
      id: 'observed-mismatch', tone: 'danger', href: '#power-path',
      text: <><strong>Observed and recorded disagree:</strong> load is observed on {seen.map((item, index) => <span key={item.key}>{index > 0 && ', '}{item.what} of <Link to={item.link}>{item.holder}</Link></span>)}, but no Cable records {seen.length === 1 ? 'it' : 'them'}. <Link to={`?kind=Cable&q=${encodeURIComponent(seen[0].q)}`}>Cable records</Link></>,
    });
  }
  const summary = <RecordSummary>
    <SummaryParts parts={[
      feed.spec.facilityRef && (inventory.issues?.Facility ? 'Facility unavailable' : reference('Facility', namespace, feed.spec.facilityRef.name)),
      feed.spec.rackRef && (inventory.issues?.Rack ? 'Rack unavailable' : reference('Rack', namespace, feed.spec.rackRef.name)),
    ]} />
  </RecordSummary>;
  const source = telemetry?.source;
  const primary = <>
    <PowerFeedPath feed={feed} inventory={inventory} href={href} />
  </>;
  const sources = feed.spec.upstreamSources ?? [];
  const tabs: RecordTab[] = [
    { id: 'upstream', label: 'Upstream sources', count: sources.length, content: sources.length ? <ul>{sources.map((item, index) => <li key={index}>{item.system}/{item.type}/{item.id}</li>)}</ul> : <p className="inventory-muted">No upstream sources recorded.</p> },
  ];
  return { summary, issues, primary, tabs };
}

// ---- Facility ----

/** Power feeds in one dense table: Feed | Rack | Capacity, the capacity as the Rack page's compact load and percentage. */
function FeedsTable({ feeds, helpers }: { feeds: PhysicalRecord[]; helpers: DetailHelpers }) {
  const { source, now } = useTelemetry();
  const { inventory, reference, href } = helpers;
  return <div className="inventory-scroll"><Table aria-label="Facility power feeds" className="inventory-dense-table">
    <TableHeader><TableRow><TableHead>Feed</TableHead><TableHead>Rack</TableHead><TableHead>Capacity</TableHead></TableRow></TableHeader>
    <TableBody>{feeds.map(feed => <TableRow key={feed.metadata.name}>
      <TableCell><Link to={href('PowerFeed', feed.metadata.namespace, feed.metadata.name)}>{recordLabel(feed)}</Link></TableCell>
      <TableCell>{!feed.spec.rackRef ? '—' : inventory.issues?.Rack ? 'Unavailable' : reference('Rack', feed.metadata.namespace, feed.spec.rackRef.name)}</TableCell>
      <TableCell><CapacityBar feed={feed} reading={source.read({ kind: 'PowerFeed', namespace: feed.metadata.namespace, name: feed.metadata.name })} now={now} source={source} compact /></TableCell>
    </TableRow>)}</TableBody>
  </Table></div>;
}

/** Cables in one dense table: Cable | Medium | Lanes | End A | End B. A Cable that names a position another Cable also names carries a red edge. */
function CablesTable({ cables, helpers }: { cables: PhysicalRecord[]; helpers: DetailHelpers }) {
  const { inventory, reference, href } = helpers;
  // Every readable Cable's lane ends, so a duplicate outside this list still counts.
  const holders = new Map<string, Set<string>>();
  const endKey = (endpoint: CableEndpoint) => { const target = endTarget(endpoint); return target && `${target.kind}/${target.name}/${endpoint.componentID}/${endpoint.position}`; };
  for (const record of inventory.physical) if (record.kind === 'Cable' && record.metadata.namespace === cables[0]?.metadata.namespace) for (const lane of record.spec.lanes ?? []) for (const endpoint of [lane.a, lane.b]) {
    const key = endKey(endpoint);
    if (key) holders.set(key, (holders.get(key) ?? new Set()).add(record.metadata.name));
  }
  const end = (cable: PhysicalRecord, side: 'a' | 'b') => {
    const lanes = cable.spec.lanes ?? [];
    const targets = [...new Map(lanes.flatMap(lane => { const target = endTarget(lane[side]); return target ? [[`${target.kind}/${target.name}`, target] as const] : []; })).values()];
    if (!targets.length) return '—';
    return <>{targets.map((target, index) => <span key={`${target.kind}/${target.name}`}>{index > 0 && ', '}{inventory.issues?.[target.kind] ? 'Unavailable' : reference(target.kind, cable.metadata.namespace, target.name)}</span>)}{lanes.length === 1 && <span className="inventory-muted"> {lanes[0][side].componentID}</span>}</>;
  };
  return <div className="inventory-scroll"><Table aria-label="Facility cables" className="inventory-dense-table">
    <TableHeader><TableRow><TableHead>Cable</TableHead><TableHead>Medium</TableHead><TableHead>Lanes</TableHead><TableHead>End A</TableHead><TableHead>End B</TableHead></TableRow></TableHeader>
    <TableBody>{cables.map(cable => {
      const shared = (cable.spec.lanes ?? []).flatMap(lane => [lane.a, lane.b]).some(endpoint => { const key = endKey(endpoint); return !!key && (holders.get(key)?.size ?? 0) > 1; });
      const note = shared ? 'Records disagree: another Cable names the same position' : undefined;
      return <TableRow key={cable.metadata.name} data-attention={shared ? 'conflict' : undefined}>
        <TableCell title={note}><Link to={href('Cable', cable.metadata.namespace, cable.metadata.name)}>{recordLabel(cable)}</Link>{note && <span className="inventory-visually-hidden"> · {note}</span>}</TableCell>
        <TableCell>{cable.spec.medium ?? '—'}</TableCell>
        <TableCell className="inventory-numeric">{cable.spec.lanes?.length ?? 0}</TableCell>
        <TableCell>{end(cable, 'a')}</TableCell>
        <TableCell>{end(cable, 'b')}</TableCell>
      </TableRow>;
    })}</TableBody>
  </Table></div>;
}

export function facilityDetail(facility: PhysicalRecord, helpers: DetailHelpers): DetailView {
  const { inventory, entries, reference } = helpers;
  const namespace = facility.metadata.namespace;
  // Header facts: the premises codes (provider and type in the tooltip), the Site links, the geographic location.
  const codes = facility.spec.identifiers?.map(identifier => identifier.value).join(' · ');
  const sites = inventory.issues?.Site ? [] : inventory.physical.filter(record => record.kind === 'Site' && !!record.metadata.uid && record.metadata.namespace === namespace && !record.metadata.deletionTimestamp && !facility.metadata.deletionTimestamp && record.spec.facilityRefs?.some(ref => ref.name === facility.metadata.name));
  const summary = <RecordSummary><SummaryParts parts={[
    codes && <span title={facilityIdentifiers(facility)}>{codes}</span>,
    sites.length ? <>{sites.map((site, index) => <span key={site.metadata.name}>{index > 0 && ', '}{reference('Site', namespace, site.metadata.name)}</span>)}</> : facilitySites(facility, inventory),
    facility.spec.locationRef && <span title="Geographic location reference">{facility.spec.locationRef.name}</span>,
  ]} /></RecordSummary>;
  const inFacility = (name?: string) => {
    if (!name) return false;
    const rack = helpers.find('Rack', namespace, name)?.record as PhysicalRecord | undefined;
    return rack?.spec.facilityRef?.name === facility.metadata.name;
  };
  const feeds = entries.filter(entry => entry.kind === 'PowerFeed' && entry.record.metadata.namespace === namespace && ((entry.record as PhysicalRecord).spec.facilityRef?.name === facility.metadata.name)).map(entry => entry.record as PhysicalRecord);
  const cables = entries.filter(entry => entry.kind === 'Cable' && entry.record.metadata.namespace === namespace && (entry.record as PhysicalRecord).spec.lanes?.some(lane => [lane.a, lane.b].some(endpoint => {
    if (!endpoint.deviceRef) return false;
    const device = helpers.find('Device', namespace, endpoint.deviceRef.name)?.record as RawDevice | undefined;
    const location = device && devicePlacement(device, inventory)?.placement;
    return location?.facilityRef?.name === facility.metadata.name || inFacility(location?.rackRef?.name);
  }))).map(entry => entry.record as PhysicalRecord);
  const equipmentUnknown = inventory.issues?.Device || inventory.issues?.DeviceClaim;
  // Unreadable collections read "?" on their tabs; no banners.
  const tabs: RecordTab[] = [
    { id: 'racks', label: 'Racks', count: inventory.issues?.Rack ? '?' : facilityRackCount(facility, inventory), content: <FacilityRacks facility={facility} inventory={inventory} /> },
    { id: 'unracked', label: 'Without a rack', count: equipmentUnknown ? '?' : unrackedCount(facility, inventory), content: <FacilityUnrackedEquipment facility={facility} inventory={inventory} /> },
    { id: 'feeds', label: 'Power feeds', count: inventory.issues?.PowerFeed ? '?' : feeds.length, content: inventory.issues?.PowerFeed ? <p>Power feed records unavailable.</p> : feeds.length ? <FeedsTable feeds={feeds} helpers={helpers} /> : <p className="inventory-muted">No power feeds recorded.</p> },
    { id: 'cables', label: 'Cables', count: inventory.issues?.Cable ? '?' : cables.length, content: inventory.issues?.Cable ? <p>Cable records unavailable.</p> : cables.length ? <CablesTable cables={cables} helpers={helpers} /> : <p className="inventory-muted">No cables recorded.</p> },
  ];
  return { summary, issues: [], primary: null, tabs };
}

// ---- DeviceType ----

const FACE_ORDER: Record<string, number> = { Front: 0, Rear: 1 };

/**
 * The model's picture and component table, as on the Device page: the
 * faceplate drawn from the recorded layout with every port catalog only, then
 * one row per template. Hovering or focusing a row outlines its tile.
 */
function ModelComponents({ model, href }: { model: PhysicalRecord; href: DetailHelpers['href'] }) {
  const drawing = useMemo(() => modelDrawingModel(model), [model]);
  const [highlighted, setHighlighted] = useState<string>();
  const highlight = useMemo<ComponentHighlight>(() => ({ id: highlighted, set: setHighlighted, clear: id => setHighlighted(value => value === id ? undefined : value) }), [highlighted]);
  const rows = [...(model.spec.componentTemplates ?? [])].sort((a, b) => (FACE_ORDER[a.face ?? ''] ?? 2) - (FACE_ORDER[b.face ?? ''] ?? 2));
  if (!rows.length) return <p className="inventory-muted">No component templates recorded.</p>;
  const connectors = rows.some(row => row.connector);
  const positions = rows.some(row => (row.positions ?? 1) > 1);
  return <section className="inventory-connections" aria-label="Components">
    <div className="inventory-device-picture">
      {drawing.layout.recorded
        ? <FaceplateDrawing drawing={drawing} namespace={model.metadata.namespace} href={href} dense highlight={highlight} label="Model faceplate" />
        : <p className="inventory-faceplate-fallback"><span className="inventory-faceplate-fallback-label">Layout not recorded</span></p>}
    </div>
    <div className="inventory-component-panel">
      <div className="inventory-scroll inventory-connection-table" role="region" tabIndex={0} aria-label="Components table">
        <Table className="inventory-dense-table">
          <TableCaption className="inventory-visually-hidden">Components · {rows.length}</TableCaption>
          <TableHeader><TableRow>
            <TableHead>Component <span className="inventory-count">{rows.length}</span></TableHead><TableHead>Type</TableHead><TableHead>Face</TableHead>
            {connectors && <TableHead>Connector</TableHead>}
            {positions && <TableHead>Positions</TableHead>}
          </TableRow></TableHeader>
          <TableBody>{rows.map(row => <TableRow key={row.id}
            data-highlight={highlight.id === row.id ? '' : undefined}
            onPointerEnter={event => { if (event.pointerType === 'mouse') highlight.set(row.id); }}
            onPointerLeave={event => { if (event.pointerType === 'mouse') highlight.clear(row.id); }}
            onClick={() => highlight.set(row.id)}
          >
            <TableCell><span title={row.name !== row.id ? row.id : undefined}>{row.name}</span></TableCell>
            <TableCell>{typeTitle(row.type, 1)}</TableCell>
            <TableCell>{row.face ?? '?'}</TableCell>
            {connectors && <TableCell>{row.connector}</TableCell>}
            {positions && <TableCell className="inventory-numeric">{(row.positions ?? 1) > 1 ? row.positions : ''}</TableCell>}
          </TableRow>)}</TableBody>
        </Table>
      </div>
    </div>
  </section>;
}

export function deviceTypeDetail(model: PhysicalRecord, helpers: DetailHelpers): DetailView {
  const { inventory, reference } = helpers;
  const namespace = model.metadata.namespace;
  const shape = model.spec.modelShape;
  // One facts strip: manufacturer · model key · size. A zero-U strip has no rack depth.
  const zeroU = shape?.heightUnits === 0;
  const summary = <RecordSummary><SummaryParts parts={[
    model.spec.manufacturerRef && reference('Manufacturer', namespace, model.spec.manufacturerRef.name),
    model.spec.model && <span title="Model key">{model.spec.model}</span>,
    zeroU ? 'Zero-U' : shape?.heightUnits === undefined ? 'Height unknown' : `${shape.heightUnits}U`,
    !zeroU && (shape?.fullDepth === false ? 'Half depth' : shape?.fullDepth ? 'Full depth' : 'Depth unknown'),
  ]} /></RecordSummary>;
  const devices = inventory.issues?.Device ? [] : helpers.entries.filter(entry => entry.kind === 'Device' && entry.record.metadata.namespace === namespace && (entry.record as RawDevice).spec.deviceTypeRef?.name === model.metadata.name).map(entry => entry.record as RawDevice);
  const tabs: RecordTab[] = [
    { id: 'devices', label: 'Devices', count: inventory.issues?.Device ? '?' : devices.length, content: inventory.issues?.Device ? <p>Device records unavailable.</p> : devices.length ? <DeviceTable devices={devices} inventory={inventory} label="Devices of this type" withFacility={new Set(devices.flatMap(device => { const at = devicePlacement(device, inventory); const facility = at && placementFacility(at.placement, namespace, inventory); return facility ? [facility] : []; })).size > 1} /> : <p className="inventory-muted">No devices of this type.</p> },
  ];
  return { summary, issues: [], primary: <ModelComponents model={model} href={helpers.href} />, tabs };
}

// ---- DeviceClaim ----

export function claimDetail(claim: RawDeviceClaim, helpers: DetailHelpers): DetailView {
  const { inventory, reference } = helpers;
  const namespace = claim.metadata.namespace;
  const slot = helpers.slotFor({ kind: 'DeviceClaim', record: claim });
  const location = claim.spec.placement;
  const issues: RecordIssue[] = [];
  if (slot && slot.devices.length > 1) issues.push({ id: 'registrations', text: 'Multiple devices reference this claim. All registrations are retained; the active device and its placement are unknown.' });
  const planned = <PlacementLinks placement={location} namespace={namespace} helpers={helpers} />;
  const scopeNote = location.scope === 'Facility' ? 'rack not recorded' : location.scope === 'RackMembership' ? 'no reserved units' : undefined;
  const devices = slot?.devices ?? [];
  const registered = inventory.issues?.Device ? 'Registration status unavailable' : devices.length === 1 ? '1 device registered' : devices.length ? `${devices.length} devices registered` : 'No device registered';
  const summary = <RecordSummary badges={[{ label: claim.spec.lifecycle, tone: 'muted' }]}>{registered}</RecordSummary>;
  const primary = <Facts label="Target and status" items={[
    { label: 'Planned placement (intent / history)', value: <>{planned}{scopeNote && ` · ${scopeNote}`}</>, always: true },
    { label: 'Expected hardware', value: modelLabel(claim.spec.expectedIdentity), always: true },
    { label: 'Expected serial', value: claim.spec.expectedIdentity.serial },
    { label: 'Registered devices', always: true, value: inventory.issues?.Device ? 'Device details unavailable' : devices.length ? <ul className="inventory-link-list">{devices.map(device => <li key={device.metadata.name}>{reference('Device', namespace, device.metadata.name)}</li>)}</ul> : 'No device registered' },
  ]} />;
  const cableRefs = claim.spec.cableRefs ?? [];
  const tabs: RecordTab[] = [
    { id: 'connections', label: 'Planned connections', count: cableRefs.length, content: cableRefs.length ? <ul>{cableRefs.map(cable => <li key={cable.name}>{reference('Cable', namespace, cable.name)}</li>)}</ul> : <p className="inventory-muted">No cable references recorded.</p> },
  ];
  return { summary, issues, primary, tabs };
}

export { recordLabel };
