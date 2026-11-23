// Per-kind detail pages, following "Page design rules" in
// docs/development/staff-inventory-ui.md. Each kind returns a one-line
// summary, issue banners, one primary view and secondary tabs. Generated
// identity lives in "About this record" (see inventory-record-layout.tsx).
import { Title } from '@datum-cloud/datum-ui/typography';
import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { legacyPeerLabel, modelLabel, slotKey, type Inventory, type InventorySlot, type RawDevice, type RawDeviceClaim } from '../inventory';
import type { CableEndpoint, ComponentTemplate, PhysicalComponent, PhysicalRecord } from '../../../shared/fleet-api';
import { devicePlacement, placementFacility, type EquipmentPlacement } from '../lib/device-placement';
import { deviceCatalog, deviceModelLabel } from '../lib/hardware-catalog';
import { depthLabel, deviceDepth } from '../lib/device-depth';
import { deviceConnections, stateCounts } from '../lib/device-connections';
import { facilityIdentifiers, facilitySites } from '../lib/site-membership';
import { displayLabel, sharedValue } from '../lib/display-names';
import { DeviceConnections } from './device-connections';
import { FacilityRacks, FacilityUnrackedEquipment, unrackedCount } from './inventory-location';
import { Facts, RecordSummary, TabLink, SummaryParts, ViewLegend, historyTab, type FactItem, type RecordIssue, type RecordTab, type SummaryBadge } from './inventory-record-layout';
import { observedDisagrees, type TelemetryContextValue } from '../lib/telemetry';
import { feedPath } from '../lib/power-paths';
import { DeviceDraw, PowerFeedPath } from './power-feed-path';

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

const TYPE_TITLES: Record<PhysicalComponent['type'], [string, string]> = {
  NetworkPort: ['Network port', 'Network ports'],
  PowerInlet: ['Power inlet', 'Power inlets'],
  PowerOutlet: ['Power outlet', 'Power outlets'],
  ConsolePort: ['Console port', 'Console ports'],
  ConsoleServerPort: ['Console server port', 'Console server ports'],
  FrontPort: ['Front pass-through port', 'Front pass-through ports'],
  RearPort: ['Rear pass-through port', 'Rear pass-through ports'],
  Module: ['Module', 'Modules'],
  InventoryItem: ['Inventory item', 'Inventory items'],
  DeviceBay: ['Device bay', 'Device bays'],
};
const typeOrder = Object.keys(TYPE_TITLES) as PhysicalComponent['type'][];
export const typeTitle = (type: PhysicalComponent['type'], count = 2) => (TYPE_TITLES[type] ?? [type, type])[count === 1 ? 0 : 1];

function byType<T extends { type: PhysicalComponent['type'] }>(items: T[]) {
  return typeOrder.map(type => ({ type, items: items.filter(item => item.type === type) })).filter(group => group.items.length);
}

/** "2 power inlets · 3 network ports". */
export function componentCounts(items: { type: PhysicalComponent['type'] }[]) {
  return byType(items).map(group => `${group.items.length} ${typeTitle(group.type, group.items.length).toLowerCase()}`).join(' · ');
}

/** Components grouped by type; the type word and a shared connector are said once in the group header. */
export function ComponentGroups({ components, label }: { components: PhysicalComponent[]; label: string }) {
  if (!components.length) return <p className="inventory-muted">No components recorded.</p>;
  return <div className="inventory-component-groups" aria-label={label} role="group">
    {byType(components).map(group => {
      const connector = sharedValue(group.items.map(item => item.connector));
      const multi = group.items.some(item => (item.positions ?? 1) > 1);
      const identity = group.items.some(item => item.serial || item.assetTag);
      return <section key={group.type} className="inventory-component-group" aria-label={typeTitle(group.type)}>
        <h4>{typeTitle(group.type, group.items.length)} <span className="inventory-count">{group.items.length}{connector ? ` · ${connector}` : ''}</span></h4>
        <ul>
          {group.items.map(item => <li key={item.id}>
            <span className="inventory-component-name">{item.name}{item.name !== item.id && <small>{item.id}</small>}</span>
            {!connector && <span>{item.connector ?? 'Connector not recorded'}</span>}
            {multi && <span>{item.positions ?? 1} positions</span>}
            {identity && <span>{[item.serial && `Serial ${item.serial}`, item.assetTag && `Asset tag ${item.assetTag}`].filter(Boolean).join(' · ') || 'No serial or asset tag recorded'}</span>}
          </li>)}
        </ul>
      </section>;
    })}
  </div>;
}

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

function cablesAt(inventory: Inventory, namespace: string | undefined, match: (endpoint: CableEndpoint) => boolean) {
  if (inventory.issues?.Cable) return [];
  return inventory.physical.filter(record => record.kind === 'Cable' && record.metadata.namespace === namespace && record.spec.lanes?.some(lane => match(lane.a) || match(lane.b)));
}

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
  const join = deviceConnections(device, inventory);
  const counts = stateCounts(join.links);
  const components = device.spec.components ?? [];
  const cables = cablesAt(inventory, namespace, endpoint => endpoint.deviceRef?.name === device.metadata.name);
  const depth = deviceDepth(device, inventory);
  const report = device.spec.reportedPlacement;

  const issues: RecordIssue[] = [];
  if (device.spec.identity.unidentified) issues.push({ id: 'identity', tone: 'danger', text: `No serial or system UUID recorded · ${device.spec.identity.unidentifiedReason ?? 'reason not recorded'} · cannot back machine capacity` });
  if (counts.conflict) issues.push({ id: 'conflict', tone: 'danger', text: `Records disagree on ${counts.conflict} ${counts.conflict === 1 ? 'connection' : 'connections'}: more than one Cable names the same port.`, href: '#device-connections' });
  if (counts.unavailable) issues.push({ id: 'peer', text: `${counts.unavailable} ${counts.unavailable === 1 ? 'connection reaches a record' : 'connections reach records'} that is missing or unreadable.`, href: '#device-connections' });
  if (join.cableAccess === 'denied') issues.push({ id: 'cables', text: 'Cable records are unavailable to you, so connections are unknown, not free.' });
  if (inventory.issues?.DeviceClaim) issues.push({ id: 'claim', text: 'Claim details unavailable; location may be incomplete.', tab: 'registration' });
  else if (!projected) issues.push({ id: 'location', text: slot ? 'Location unknown: no reported placement or planned location.' : 'Location unknown: no reported placement, and the claim record was not found.', tab: 'location' });
  else if (projected.placement.rackRef && projected.placement.position === undefined && !projected.placement.mount) issues.push({ id: 'position', text: 'Position in the rack is unknown.', tab: 'location' });
  if (slot && slot.devices.length > 1) issues.push({ id: 'registrations', text: 'Multiple devices reference this claim. All registrations are retained; the active device and its placement are unknown.', tab: 'registration' });
  if (device.spec.deviceTypeRef && !catalog) issues.push({ id: 'catalog', text: 'Catalog unavailable · showing recorded observation.', tab: 'registration' });

  const summary = <RecordSummary badges={lifecycleBadge(device.spec.lifecycle)}>
    {projected ? <>
      <span className="inventory-muted">{projected.basis === 'Reported placement' ? 'Reported at ' : 'Planned at (claim-derived fallback) '}</span>
      <PlacementLinks placement={projected.placement} namespace={namespace} helpers={helpers} />
    </> : <span>Location unknown</span>}
  </RecordSummary>;

  const model = catalog ? reference('DeviceType', namespace, catalog.model.metadata.name) : deviceModelLabel(device, inventory);
  const primary = <>
    <Facts label="Identity" items={[
      { label: 'Model', value: catalog ? <>{recordLabel(catalog.manufacturer)} {model}</> : model, always: true },
      { label: 'Serial number', value: device.spec.identity.serial },
      { label: 'Asset tag', value: device.spec.assetTag },
    ]} />
    {components.length > 0 && <p className="inventory-muted">{componentCounts(components)} · <TabLink tab="components">All components</TabLink></p>}
    {components.some(component => component.type === 'PowerInlet') && <p className="inventory-device-draw"><DeviceDraw device={device} /></p>}
    <div id="device-connections"><DeviceConnections key={slotKey(namespace, device.metadata.name)} device={device} inventory={inventory} href={href} /></div>
  </>;

  const tabs: RecordTab[] = [
    { id: 'components', label: 'Components', count: components.length, content: <ComponentGroups components={components} label="Physical components" /> },
    {
      id: 'cables', label: 'Cable records', count: inventory.issues?.Cable ? '?' : cables.length, content: inventory.issues?.Cable
        ? <p>Cable records are unavailable to you. This does not mean the Device has no cables.</p>
        : <><RecordLinks kind="Cable" records={cables} helpers={helpers} empty="No Cable records name this Device. That does not establish that it has no cables." />
          <ViewLegend>Cable records, not live link state.</ViewLegend></>,
    },
    {
      id: 'location', label: 'Location', content: <>
        <Facts items={[
          { label: 'Location / Reported placement', value: helpers.placement({ kind: 'Device', record: device }), always: true },
          { label: 'Placement report', value: report && `Revision ${report.revision} · ${report.observedAt}, not physically verified · Reason: ${report.reason} · Evidence: ${report.reference}` },
          { label: 'Depth for rack conflicts', value: `${depthLabel(depth)} · ${depth.full ? 'occupies both rack faces' : 'occupies its mounted face only'} · not measured`, always: true },
          { label: 'Catalog nominal height', value: catalog?.model.spec.modelShape?.heightUnits !== undefined && `${catalog.model.spec.modelShape.heightUnits}U · not measured or used for occupancy` },
          { label: 'Recorded nominal dimensions', value: device.spec.modelShape && `${device.spec.modelShape.heightUnits ?? 'Unknown'}U · ${device.spec.modelShape.fullDepth === undefined ? 'Depth not recorded' : device.spec.modelShape.fullDepth ? 'Full depth' : 'Partial depth'}` },
        ]} />
        <ViewLegend>Recordkeeping is not a physical move or a capacity reservation.</ViewLegend>
        {locationEditor}
      </>,
    },
    {
      id: 'registration', label: 'Registration', content: <>
        <Facts items={[
          { label: 'Claim / registration history', value: inventory.issues?.DeviceClaim ? 'Claim details unavailable' : reference('DeviceClaim', namespace, device.spec.claimRef.name), always: true },
          { label: 'Shared catalog', value: catalog ? reference('DeviceType', namespace, catalog.model.metadata.name) : device.spec.deviceTypeRef ? 'Catalog unavailable · showing recorded observation' : 'Not assigned · showing recorded observation', always: true },
          { label: 'Recorded hardware observation', value: modelLabel(device.spec.identity) },
          { label: 'System UUID', value: device.spec.identity.systemUUID },
          { label: 'Management endpoint', value: device.spec.bmc?.endpoint },
          { label: 'Provider host reference', value: device.spec.providerHostRef && `${device.spec.providerHostRef.system}/${device.spec.providerHostRef.type}/${device.spec.providerHostRef.id}` },
        ]} />
        {!!device.spec.internalMappings?.length && <><h4>Internal paths</h4><ul>{device.spec.internalMappings.map((mapping, index) => <li key={index}>{mapping.from.componentID}:{mapping.from.position} → {mapping.to.componentID}:{mapping.to.position}</li>)}</ul></>}
        {!!device.spec.serviceAssociations?.length && <><h4>Service references</h4><ul>{device.spec.serviceAssociations.map((association, index) => <li key={index}>{association.componentID} → {association.serviceRef.system}/{association.serviceRef.type}/{association.serviceRef.id}</li>)}</ul></>}
        {(!!device.spec.interfaces?.length || !!device.spec.unmappedMACAddresses?.length) && <details>
          <summary>Recorded network interfaces & legacy peers</summary>
          <p className="inventory-muted">Legacy peers are retained as history. Cable records describe typed physical connections; neither verifies a live link.</p>
          <ul>{device.spec.interfaces?.map(iface => <li key={iface.name}>{iface.name} · {iface.macAddress ?? 'MAC not recorded'}{iface.peer && ` · legacy peer ${legacyPeerLabel(iface.peer, inventory.issues)}`}</li>)}</ul>
          {!!device.spec.unmappedMACAddresses?.length && <><h4>Unmapped MAC addresses</h4><p>{device.spec.unmappedMACAddresses.join(', ')}</p></>}
        </details>}
      </>,
    },
    historyTab(device.metadata),
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
    <ViewLegend>Cable records, not live link state. Nothing here is verified.</ViewLegend>
  </section>;
  const summary = <RecordSummary><SummaryParts parts={[cable.spec.medium ?? 'Medium not recorded', `${lanes.length} ${lanes.length === 1 ? 'lane' : 'lanes'}`]} /></RecordSummary>;
  const tabs: RecordTab[] = [];
  if (plans.length) tabs.push({ id: 'plans', label: 'Installation plans', count: plans.length, content: <RecordLinks kind="DeviceClaim" records={plans} helpers={helpers} empty="" /> });
  tabs.push(historyTab(cable.metadata));
  return { summary, issues, primary, tabs };
}

// ---- PowerFeed ----

export function powerFeedDetail(feed: PhysicalRecord, helpers: DetailHelpers): DetailView {
  const { inventory, reference, href, telemetry } = helpers;
  const namespace = feed.metadata.namespace;
  const path = feedPath(feed, inventory);
  const issues: RecordIssue[] = [];
  if (inventory.issues?.Cable) issues.push({ id: 'cables', text: 'Cable records are unavailable to you, so outlet use is unknown, not free.' });
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
    <ViewLegend>
      Cable records and observed telemetry, not verified state. No cable recorded means unknown, not free; supply ratings do not establish available power, and separate feeds in records do not prove independent supply.
      {' '}Marks: ↑ above 80% of the continuous limit · ⚠︎ over it · ◷ stale · ≠ observed and recorded disagree. Telemetry: {source ? source.description : 'No telemetry source configured'}.
    </ViewLegend>
  </>;
  const sources = feed.spec.upstreamSources ?? [];
  const tabs: RecordTab[] = [
    { id: 'upstream', label: 'Upstream sources', count: sources.length, content: sources.length ? <ul>{sources.map((item, index) => <li key={index}>{item.system}/{item.type}/{item.id}</li>)}</ul> : <p className="inventory-muted">No upstream sources recorded.</p> },
    historyTab(feed.metadata),
  ];
  return { summary, issues, primary, tabs };
}

// ---- Facility ----

export function facilityDetail(facility: PhysicalRecord, helpers: DetailHelpers): DetailView {
  const { inventory, entries } = helpers;
  const namespace = facility.metadata.namespace;
  const sites = facilitySites(facility, inventory);
  const summary = <RecordSummary><SummaryParts parts={[facilityIdentifiers(facility), sites]} /></RecordSummary>;
  const issues: RecordIssue[] = [];
  if (inventory.issues?.Rack) issues.push({ id: 'racks', text: "Rack details unavailable. The facility's rack count is unknown." });
  if (inventory.issues?.Device || inventory.issues?.DeviceClaim) issues.push({ id: 'equipment', text: 'Some equipment records are unavailable; equipment counts are incomplete.' });
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
  const unracked = <FacilityUnrackedEquipment facility={facility} inventory={inventory} />;
  const tabs: RecordTab[] = [
    { id: 'unracked', label: 'Equipment without a rack', count: inventory.issues?.Device || inventory.issues?.DeviceClaim ? '?' : unrackedCount(facility, inventory), content: unracked },
    {
      id: 'power', label: 'Power & cables', count: inventory.issues?.PowerFeed || inventory.issues?.Cable ? '?' : feeds.length + cables.length, content: <>
        <h4>Power feeds <span className="inventory-count">{inventory.issues?.PowerFeed ? 'unavailable' : feeds.length}</span></h4>
        {inventory.issues?.PowerFeed ? <p>Power feed records are unavailable to you.</p> : <RecordLinks kind="PowerFeed" records={feeds} helpers={helpers} empty="No power feed records name this facility." />}
        <h4>Cables at this facility <span className="inventory-count">{inventory.issues?.Cable ? 'unavailable' : cables.length}</span></h4>
        {inventory.issues?.Cable ? <p>Cable records are unavailable to you.</p> : <RecordLinks kind="Cable" records={cables} helpers={helpers} empty="No Cable records reach equipment placed here. That does not establish that none exist." />}
      </>,
    },
    historyTab(facility.metadata),
  ];
  return { summary, issues, primary: <FacilityRacks facility={facility} inventory={inventory} />, tabs, about: [{ label: 'Geographic location reference', value: facility.spec.locationRef?.name }] };
}

// ---- DeviceType ----

export function deviceTypeDetail(model: PhysicalRecord, helpers: DetailHelpers): DetailView {
  const { inventory, reference } = helpers;
  const namespace = model.metadata.namespace;
  const shape = model.spec.modelShape;
  const templates = model.spec.componentTemplates ?? [];
  const summary = <RecordSummary><SummaryParts parts={[
    model.spec.manufacturerRef && reference('Manufacturer', namespace, model.spec.manufacturerRef.name),
    model.spec.model && `Model key ${model.spec.model}`,
    `${shape?.heightUnits ?? 'Unknown'}U`,
    shape?.fullDepth === false ? 'Half depth' : shape?.fullDepth ? 'Full depth' : 'Depth unknown',
  ]} /></RecordSummary>;
  const faces: { face: ComponentTemplate['face'] | 'unknown'; title: string }[] = [{ face: 'Front', title: 'Chassis front' }, { face: 'Rear', title: 'Chassis rear' }, { face: 'unknown', title: 'Chassis face unknown' }];
  const primary = <>
    {faces.map(({ face, title }) => {
      const items = templates.filter(template => (template.face ?? 'unknown') === face);
      if (!items.length) return null;
      return <section key={face} className="inventory-template-face" aria-label={title}>
        <Title as="h3" level={6}>{title} <span className="inventory-count">{items.length}</span></Title>
        {byType(items).map(group => {
          const connector = sharedValue(group.items.map(item => item.connector));
          const positions = sharedValue(group.items.map(item => item.positions ?? 1));
          return <div key={group.type} className="inventory-template-group">
            <h4>{typeTitle(group.type, group.items.length)} <span className="inventory-count">{group.items.length}{connector ? ` · ${connector}` : ''}{positions && positions > 1 ? ` · ${positions} positions each` : ''}</span></h4>
            <p>{group.items.map(item => [item.name, !connector && item.connector, !positions && (item.positions ?? 1) > 1 && `${item.positions} positions`].filter(Boolean).join(' ')).join(', ')}</p>
          </div>;
        })}
      </section>;
    })}
    {!templates.length && <p className="inventory-muted">No component templates recorded.</p>}
    <ViewLegend>Reference template only: no installed assets, measured geometry or occupancy, and nothing is materialized automatically. Faces are relative to the chassis; a Device component with the same id inherits its face.</ViewLegend>
  </>;
  const devices = inventory.issues?.Device ? [] : helpers.entries.filter(entry => entry.kind === 'Device' && entry.record.metadata.namespace === namespace && (entry.record as RawDevice).spec.deviceTypeRef?.name === model.metadata.name).map(entry => entry.record as RawDevice);
  const tabs: RecordTab[] = [
    { id: 'devices', label: 'Devices', count: inventory.issues?.Device ? '?' : devices.length, content: inventory.issues?.Device ? <p>Device records are unavailable to you; the count is unknown.</p> : <RecordLinks kind="Device" records={devices} helpers={helpers} empty="No Device records reference this type." /> },
    historyTab(model.metadata),
  ];
  return { summary, issues: [], primary, tabs, about: [{ label: 'Normalized model identity', value: model.spec.model }] };
}

// ---- DeviceClaim ----

export function claimDetail(claim: RawDeviceClaim, helpers: DetailHelpers): DetailView {
  const { inventory, reference } = helpers;
  const namespace = claim.metadata.namespace;
  const slot = helpers.slotFor({ kind: 'DeviceClaim', record: claim });
  const location = claim.spec.placement;
  const issues: RecordIssue[] = [];
  if (slot && slot.devices.length > 1) issues.push({ id: 'registrations', text: 'Multiple devices reference this claim. All registrations are retained; the active device and its placement are unknown.' });
  if (!location.scope && (location.facility || location.rack)) issues.push({ id: 'legacy', text: 'Placement is legacy text only; no typed facility or rack is linked.' });
  const planned: ReactNode = location.scope
    ? <PlacementLinks placement={location} namespace={namespace} helpers={helpers} />
    : helpers.placement({ kind: 'DeviceClaim', record: claim });
  const scopeNote = location.scope === 'Facility' ? 'rack not recorded' : location.scope === 'RackMembership' ? 'no reserved units' : undefined;
  const devices = slot?.devices ?? [];
  const registered = inventory.issues?.Device ? 'Registration status unavailable' : devices.length === 1 ? '1 device registered' : devices.length ? `${devices.length} devices registered` : 'No device registered';
  const summary = <RecordSummary badges={claim.spec.lifecycle ? [{ label: claim.spec.lifecycle, tone: 'muted' }] : []}>{registered}</RecordSummary>;
  const primary = <Facts label="Target and status" items={[
    { label: 'Planned placement (intent / history)', value: <>{planned}{scopeNote && ` · ${scopeNote}`}</>, always: true },
    { label: 'Expected hardware', value: modelLabel(claim.spec.expectedIdentity), always: true },
    { label: 'Expected serial', value: claim.spec.expectedIdentity.serial },
    { label: 'Registered devices', always: true, value: inventory.issues?.Device ? 'Device details unavailable' : devices.length ? <ul className="inventory-link-list">{devices.map(device => <li key={device.metadata.name}>{reference('Device', namespace, device.metadata.name)}</li>)}</ul> : 'No device registered' },
  ]} />;
  const cableRefs = claim.spec.cableRefs ?? [];
  const ports = claim.spec.ports ?? [];
  const tabs: RecordTab[] = [
    { id: 'connections', label: 'Planned connections', count: cableRefs.length, content: cableRefs.length ? <ul>{cableRefs.map(cable => <li key={cable.name}>{reference('Cable', namespace, cable.name)}</li>)}</ul> : <p className="inventory-muted">No cable references recorded.</p> },
    { id: 'ports', label: 'Legacy expected ports', count: ports.length, content: ports.length ? <ul>{ports.map(port => <li key={port.name}>{port.name} · {port.type ?? 'Type not recorded'} · {port.speedMbps ?? 'Unknown'} Mb/s{port.expectedPeer && ` → ${legacyPeerLabel(port.expectedPeer, inventory.issues)}`}</li>)}</ul> : <p className="inventory-muted">No expected ports recorded.</p> },
    historyTab(claim.metadata),
  ];
  return { summary, issues, primary, tabs };
}

export { recordLabel };
