// Directory rows: one dense line per record, with the columns an operator
// scans each kind for. Cells hold plain text, so search matches what the row
// shows. Unknown reads "?", never 0 or empty; a denied collection names
// nothing. A row that needs checking carries a colour edge: red when records
// disagree, amber for a missing or unreadable peer. The reason is the cell or
// row tooltip and is part of the row's accessible name.

import type { CableEndpoint, Device, DeviceClaim, PhysicalRecord } from '../../../shared/fleet-api';
import { modelLabel, slotKey, type Inventory } from '../inventory';
import { devicePlacement, equipmentGeometry, equipmentPlacements, placementFacility, type Placement } from '../lib/device-placement';
import { deviceCatalog } from '../lib/hardware-catalog';
import { catalogLabel, manufacturerInventory } from '../lib/manufacturer-inventory';
import { feedCapacity, formatPower, loadLevel, LOAD_WORDS } from '../lib/power-capacity';
import { classifyFeeds, FEED_CLASS_WORDS, psuFeeds } from '../lib/power-paths';
import { formatAge, valid, type TelemetryContextValue } from '../lib/telemetry';
import { inventoryHref, rackPlacements } from './inventory-location';
import { rackIssues } from './rack-detail';

export type Attention = 'conflict' | 'unavailable';
export interface Cell { text: string; to?: string; title?: string; attention?: Attention; numeric?: boolean }
export interface DirectoryRow {
  cells: Record<string, Cell | undefined>;
  /** A short muted word after the name, such as a non-Active lifecycle. */
  note?: Cell;
  attention?: Attention;
  reasons: string[];
  /** Sort keys after which the name breaks ties. Directories without one sort by name. */
  order?: (string | number)[];
}
export interface DirectoryColumn { head: string; optional?: boolean }

type Kind = 'Site' | 'Device' | 'Facility' | 'Rack' | 'DeviceClaim' | 'Cable' | 'PowerFeed' | 'Manufacturer' | 'DeviceType';
type Entry = { kind: Kind; record: Device | DeviceClaim | PhysicalRecord };

const NONE: Cell = { text: '—' };
const unknown = (title: string, attention?: Attention): Cell => ({ text: '?', title, attention });
const count = (value: number | undefined, title: string): Cell => value === undefined ? unknown(title) : { text: String(value), numeric: true };
const units = (position: number, height = 1) => `U${position}${height > 1 ? `–U${position + height - 1}` : ''}`;
const KIND_WORDS: Record<Kind, string> = { Site: 'Operational site', Device: 'Device', Facility: 'Facility', Rack: 'Rack', DeviceClaim: 'Installation plan', Cable: 'Cable', PowerFeed: 'Power feed', Manufacturer: 'Manufacturer', DeviceType: 'Device type' };

export function directoryColumns(kind: Kind, inventory: Inventory): DirectoryColumn[] {
  const facilities = inventory.physical.filter(record => record.kind === 'Facility').length > 1 ? [{ head: 'Facility' }] : [];
  switch (kind) {
    case 'Device': return [...facilities, { head: 'Rack' }, { head: 'U' }, { head: 'Face' }, { head: 'Model' }, { head: 'Serial', optional: true }, { head: 'Asset tag', optional: true }, { head: 'Power', optional: true }];
    case 'Cable': return [{ head: 'Medium' }, { head: 'A end' }, { head: 'B end' }, { head: 'Lanes', optional: true }];
    case 'PowerFeed': return [{ head: 'Location' }, { head: 'Type' }, { head: 'Voltage' }, { head: 'Breaker' }, { head: 'Capacity' }, { head: 'Load' }];
    case 'Site': return [{ head: 'Code' }, { head: 'Facilities' }, { head: 'Racks' }, { head: 'Devices' }];
    case 'Facility': return [{ head: 'Code' }, { head: 'Site' }, { head: 'Racks' }, { head: 'Devices' }];
    case 'Rack': return [...facilities, { head: 'Height' }, { head: 'Devices' }, { head: 'Used' }, { head: 'Power' }, { head: 'Feeds' }];
    case 'DeviceClaim': return [...facilities, { head: 'Rack' }, { head: 'U' }, { head: 'Face' }, { head: 'Model' }, { head: 'Device' }, { head: 'State' }];
    case 'Manufacturer': return [{ head: 'Aliases', optional: true }, { head: 'Device types' }, { head: 'Devices' }];
    case 'DeviceType': return [{ head: 'Manufacturer' }, { head: 'Model key' }, { head: 'Height' }, { head: 'Depth' }, { head: 'Ports' }, { head: 'Devices' }];
  }
}

export function directoryRows(entries: Entry[], inventory: Inventory, telemetry: TelemetryContextValue): Map<Entry, DirectoryRow> {
  const devices = [...inventory.slots.flatMap(slot => slot.devices), ...inventory.unboundDevices];
  const placements = equipmentPlacements(inventory);
  const record = (kind: Kind, namespace: string | undefined, name: string): Entry['record'] | undefined => {
    const key = slotKey(namespace, name);
    const pool: Entry['record'][] = kind === 'Device' ? devices : kind === 'DeviceClaim' ? inventory.slots.map(slot => slot.claim) : inventory.physical.filter(item => item.kind === kind);
    return pool.find(item => slotKey(item.metadata.namespace, item.metadata.name) === key);
  };
  const label = (kind: Kind, target: Entry['record']) => kind === 'Manufacturer' || kind === 'DeviceType'
    ? catalogLabel(target, inventory.physical.filter(item => item.kind === kind && item.metadata.namespace === target.metadata.namespace))
    : target.spec.displayName || target.metadata.name;
  /** A link to a related record; "?" when its collection is unreadable, the reference name when it is missing. */
  const link = (kind: Kind, namespace: string | undefined, name: string | undefined): Cell => {
    if (!name) return NONE;
    if (inventory.issues?.[kind]) return unknown(`${KIND_WORDS[kind]} records unavailable`, 'unavailable');
    const target = record(kind, namespace, name);
    return target ? { text: label(kind, target), to: inventoryHref(kind, namespace, name) } : { text: name, title: `${KIND_WORDS[kind]} record not found`, attention: 'unavailable' };
  };

  /** Devices whose placement is in one of these facilities; undefined when any input is unreadable. */
  const devicesIn = (namespace: string | undefined, facilities: Set<string>) => {
    if (inventory.issues?.Device || inventory.issues?.DeviceClaim || inventory.issues?.Rack || inventory.issues?.Facility) return undefined;
    const names = new Set(placements.filter(slot => slot.record.metadata.namespace === namespace && facilities.has(placementFacility(slot.placement, namespace, inventory) ?? '')).flatMap(slot => slot.devices.map(device => device.metadata.name)));
    return names.size;
  };
  const racksIn = (namespace: string | undefined, facilities: Set<string>) => inventory.issues?.Rack || inventory.issues?.Facility ? undefined
    : inventory.physical.filter(item => item.kind === 'Rack' && item.metadata.namespace === namespace && facilities.has(item.spec.facilityRef?.name ?? '')).length;

  /** Rack, units and face of a placement, shared by Devices and Installation plans. */
  const where = (placement: Placement, namespace: string | undefined): Record<string, Cell> => {
    const facility = placementFacility(placement, namespace, inventory);
    return {
      Facility: inventory.issues?.Facility ? unknown('Facility records unavailable') : link('Facility', namespace, facility),
      Rack: link('Rack', namespace, placement.rackRef?.name),
      U: placement.mount ? { text: placement.mount, title: 'Non-rack-unit mount' }
        : placement.position !== undefined ? { text: units(placement.position, placement.height), numeric: true }
          : placement.rackRef ? unknown('Position not recorded', 'unavailable') : NONE,
      Face: placement.face ? { text: placement.face } : placement.scope === 'RackUnits' ? unknown('Face not recorded') : NONE,
    };
  };
  const top = (placement: Placement) => placement.position === undefined ? -1 : placement.position + (placement.height ?? 1) - 1;

  // Cable ends recorded by more than one cable: the records disagree.
  const endKey = (namespace: string | undefined, end: CableEndpoint) => `${namespace ?? ''}/${end.deviceRef ? `Device/${end.deviceRef.name}` : end.powerFeedRef ? `PowerFeed/${end.powerFeedRef.name}` : '?'}/${end.componentID}`;
  const endOwners = new Map<string, Set<string>>();
  for (const cable of inventory.physical.filter(item => item.kind === 'Cable')) for (const lane of cable.spec.lanes ?? []) for (const end of [lane.a, lane.b]) {
    if (!end.deviceRef && !end.powerFeedRef) continue;
    const key = endKey(cable.metadata.namespace, end);
    endOwners.set(key, (endOwners.get(key) ?? new Set()).add(cable.metadata.name));
  }

  /** Measured load of a feed against its continuous limit: "706 VA · 14%". */
  const feedLoad = (feed: PhysicalRecord) => {
    const capacity = feedCapacity(feed.spec.supply);
    const reading = telemetry.source.read({ kind: 'PowerFeed', namespace: feed.metadata.namespace, name: feed.metadata.name });
    const apparent = valid(reading?.apparentPowerVA, telemetry.now);
    const real = valid(reading?.powerW, telemetry.now);
    const measured = capacity.phase === 'dc' ? real : apparent ?? real;
    const unit = capacity.phase === 'dc' || !apparent ? 'W' as const : 'VA' as const;
    return { capacity, measured, unit };
  };

  const rows = new Map<Entry, DirectoryRow>();
  for (const entry of entries) {
    const namespace = entry.record.metadata.namespace;
    const row: DirectoryRow = { cells: {}, reasons: [] };
    const flag = (attention: Attention, reason: string) => { row.reasons.push(reason); if (attention === 'conflict' || !row.attention) row.attention = attention; };
    switch (entry.kind) {
      case 'Device': {
        const device = entry.record as Device;
        const projected = devicePlacement(device, inventory);
        const catalog = deviceCatalog(device, inventory);
        if (projected) {
          Object.assign(row.cells, where(projected.placement, namespace));
          if (projected.basis === 'Claim-derived fallback') flag('unavailable', 'Planned location from the installation plan; no placement reported');
          if (projected.multipleRegistrations) flag('conflict', 'Multiple registrations; the active device is unknown');
        } else {
          const reason = inventory.issues?.DeviceClaim ? 'Installation plans unavailable and no placement reported' : 'Installation plan not found and no placement reported';
          Object.assign(row.cells, { Facility: unknown(reason), Rack: unknown(reason), U: unknown(reason), Face: unknown(reason) });
          flag('unavailable', reason);
        }
        row.cells.Model = { text: catalog ? label('DeviceType', catalog.model) : modelLabel(device.spec.identity) };
        row.cells.Serial = device.spec.identity.serial ? { text: device.spec.identity.serial, numeric: true } : device.spec.identity.unidentified ? { text: 'None', title: device.spec.identity.unidentifiedReason ?? 'No hardware identity' } : undefined;
        row.cells['Asset tag'] = device.spec.assetTag ? { text: device.spec.assetTag, numeric: true } : undefined;
        if (device.spec.components?.some(component => component.type === 'PowerInlet')) {
          const psus = psuFeeds(device, inventory);
          const classified = classifyFeeds(psus);
          const feeds = [...new Set(psus?.flatMap(psu => psu.feed ? [psu.feed.label] : []) ?? [])];
          row.cells.Power = !psus ? unknown('Cable records unavailable') : { text: [...feeds, ...(psus.some(psu => !psu.feed) ? ['?'] : [])].join(' + '), title: classified ? FEED_CLASS_WORDS[classified.class] : undefined };
        }
        if (device.spec.lifecycle && device.spec.lifecycle !== 'Active') row.note = { text: device.spec.lifecycle, title: `Lifecycle: ${device.spec.lifecycle}` };
        const placement = projected?.placement;
        // Location order, as in the elevation: facility, rack, then top of rack first; unplaced last.
        row.order = placement ? [0, row.cells.Facility?.text ?? '', placement.rackRef ? row.cells.Rack!.text : '\uffff', -top(placement)] : [1];
        break;
      }
      case 'DeviceClaim': {
        const claim = entry.record as DeviceClaim;
        const slot = inventory.slots.find(item => item.key === slotKey(namespace, claim.metadata.name));
        Object.assign(row.cells, where(claim.spec.placement, namespace));
        if (claim.spec.placement.scope === 'RackUnits' && claim.spec.placement.position === undefined) row.cells.U = unknown('Position not recorded', 'unavailable');
        const { manufacturer, model } = claim.spec.expectedIdentity;
        const type = inventory.issues?.DeviceType ? undefined : inventory.physical.find(item => item.kind === 'DeviceType' && item.metadata.namespace === namespace && item.spec.model === model && (!manufacturer || item.spec.manufacturerRef?.name === manufacturer));
        const maker = manufacturer && !inventory.issues?.Manufacturer ? record('Manufacturer', namespace, manufacturer) : undefined;
        row.cells.Model = { text: type ? label('DeviceType', type) : maker ? `${label('Manufacturer', maker)} ${model}` : modelLabel(claim.spec.expectedIdentity) };
        const registered = slot?.devices ?? [];
        row.cells.Device = inventory.issues?.Device ? unknown('Device records unavailable')
          : registered.length === 1 ? { text: label('Device', registered[0]), to: inventoryHref('Device', namespace, registered[0].metadata.name) }
            : registered.length ? { text: `${registered.length} devices`, title: 'Multiple registrations; the active device is unknown', attention: 'conflict' } : NONE;
        row.cells.State = inventory.issues?.Device ? unknown('Device records unavailable') : { text: registered.length ? 'Registered' : 'Planned' };
        const differs = registered.filter(device => {
          const report = device.spec.reportedPlacement;
          if (!report) return false;
          const plan = claim.spec.placement;
          return (report.rackRef?.name ?? '') !== (plan.rackRef?.name ?? '') || (plan.position !== undefined && report.position !== plan.position) || (plan.face !== undefined && report.face !== plan.face);
        });
        if (differs.length) flag('unavailable', 'Reported placement differs from the plan');
        row.order = [0, row.cells.Facility?.text ?? '', claim.spec.placement.rackRef ? row.cells.Rack!.text : '\uffff', -top(claim.spec.placement)];
        break;
      }
      case 'Cable': {
        const cable = entry.record as PhysicalRecord;
        const lanes = cable.spec.lanes ?? [];
        const target = (end: CableEndpoint): Cell & { kind?: Kind; name?: string } => {
          const kind: Kind | undefined = end.deviceRef ? 'Device' : end.powerFeedRef ? 'PowerFeed' : undefined;
          if (!kind) return unknown('Endpoint not recorded', 'unavailable');
          return { ...link(kind, namespace, end.deviceRef?.name ?? end.powerFeedRef!.name), kind, name: end.deviceRef?.name ?? end.powerFeedRef!.name };
        };
        const side = (key: 'a' | 'b'): Cell => {
          if (!lanes.length) return unknown('No lanes recorded', 'unavailable');
          const targets = lanes.map(lane => target(lane[key]));
          const ports = [...new Set(lanes.map(lane => lane[key].componentID))].join(', ');
          const names = new Set(targets.map(item => item.text));
          const shared = names.size === 1 ? targets[0] : undefined;
          const attention = targets.find(item => item.attention)?.attention;
          const reasons = [...new Set(targets.flatMap(item => item.title ? [item.title] : []))];
          const perLane = lanes.length > 1 ? lanes.map((lane, index) => `${lane.id}: ${targets[index].text} · ${lane[key].componentID}`) : [];
          return {
            text: `${shared ? shared.text : `${names.size} ${targets.every(item => item.kind === 'PowerFeed') ? 'feeds' : 'devices'}`} · ${ports}`,
            to: shared?.to, attention, title: [...reasons, ...perLane].join('\n') || undefined,
          };
        };
        row.cells.Medium = cable.spec.medium ? { text: cable.spec.medium } : unknown('Medium not recorded');
        row.cells['A end'] = side('a');
        row.cells['B end'] = side('b');
        row.cells.Lanes = lanes.length > 1 ? { text: String(lanes.length), numeric: true } : undefined;
        if (lanes.some(lane => [lane.a, lane.b].some(end => (endOwners.get(endKey(namespace, end))?.size ?? 0) > 1))) flag('conflict', 'Another cable records the same port');
        break;
      }
      case 'PowerFeed': {
        const feed = entry.record as PhysicalRecord;
        const supply = feed.spec.supply;
        row.cells.Location = feed.spec.rackRef ? link('Rack', namespace, feed.spec.rackRef.name) : link('Facility', namespace, feed.spec.facilityRef?.name);
        row.cells.Type = supply?.type ? { text: supply.type } : unknown('Supply not recorded');
        row.cells.Voltage = supply?.voltageV ? { text: `${supply.voltageV} V`, numeric: true } : unknown('Voltage not recorded');
        row.cells.Breaker = supply?.currentA ? { text: `${supply.currentA} A`, numeric: true } : unknown('Current rating not recorded');
        const { capacity, measured, unit } = feedLoad(feed);
        row.cells.Capacity = capacity.ratedVA !== undefined ? { text: formatPower(capacity.ratedVA, capacity.phase === 'dc' ? 'W' : 'VA'), title: capacity.formula, numeric: true } : unknown(capacity.missing ?? 'Rating unknown');
        if (!measured) row.cells.Load = unknown(telemetry.source.kind === 'none' ? `No load reported · ${telemetry.source.description}` : 'No load reported');
        else {
          const percent = capacity.continuousVA ? Math.round((measured.value / capacity.continuousVA) * 100) : undefined;
          const level = measured.state === 'fresh' ? loadLevel(measured.value, capacity.continuousVA) : undefined;
          const age = measured.state === 'stale' ? `Stale · ${formatAge(measured.ageSeconds)}` : formatAge(measured.ageSeconds);
          row.cells.Load = { text: `${formatPower(measured.value, unit)}${percent !== undefined ? ` · ${percent}%` : ''}`, numeric: true, title: [percent !== undefined && `${percent}% of continuous limit`, level && level !== 'normal' && LOAD_WORDS[level], age].filter(Boolean).join(' · '), attention: level && level !== 'normal' ? 'unavailable' : undefined };
        }
        break;
      }
      case 'Site': {
        const site = entry.record as PhysicalRecord;
        const refs = site.spec.facilityRefs ?? [];
        const found = inventory.issues?.Facility ? undefined : refs.filter(ref => record('Facility', namespace, ref.name));
        const members = new Set(found?.map(ref => ref.name) ?? []);
        row.cells.Code = site.spec.code ? { text: site.spec.code } : NONE;
        row.cells.Facilities = count(found?.length, 'Facility records unavailable');
        row.cells.Racks = count(found && racksIn(namespace, members), 'Facility or rack records unavailable');
        row.cells.Devices = count(found && devicesIn(namespace, members), 'Some placement records are unavailable');
        if (found && found.length < refs.length) flag('unavailable', `${refs.length - found.length} facility ${refs.length - found.length === 1 ? 'membership' : 'memberships'} unresolved`);
        break;
      }
      case 'Facility': {
        const facility = entry.record as PhysicalRecord;
        const identifiers = facility.spec.identifiers ?? [];
        const codes = identifiers.filter(item => item.type === 'PremisesCode');
        const shown = codes.length ? codes : identifiers;
        row.cells.Code = shown.length ? { text: shown.map(item => item.value).join(' · '), title: shown.map(item => `${item.provider} · ${item.value}`).join('\n') } : NONE;
        const sites = inventory.issues?.Site ? undefined : inventory.physical.filter(item => item.kind === 'Site' && item.metadata.namespace === namespace && item.spec.facilityRefs?.some(ref => ref.name === facility.metadata.name));
        row.cells.Site = !sites ? unknown('Operational site records unavailable') : sites.length === 1 ? { text: label('Site', sites[0]), to: inventoryHref('Site', namespace, sites[0].metadata.name) } : sites.length ? { text: sites.map(item => label('Site', item)).join(', ') } : NONE;
        const members = new Set([facility.metadata.name]);
        row.cells.Racks = count(racksIn(namespace, members), 'Rack records unavailable');
        row.cells.Devices = count(devicesIn(namespace, members), 'Some placement records are unavailable');
        break;
      }
      case 'Rack': {
        const rack = entry.record as PhysicalRecord;
        const height = rack.spec.heightUnits;
        const slots = rackPlacements(rack, inventory);
        const placementsDenied = !!inventory.issues?.Device || !!inventory.issues?.DeviceClaim;
        row.cells.Facility = link('Facility', namespace, rack.spec.facilityRef?.name);
        row.cells.Height = height ? { text: `${height}U`, numeric: true } : unknown('Height not recorded');
        row.cells.Devices = count(inventory.issues?.Device ? undefined : new Set(slots.flatMap(slot => slot.devices.map(device => device.metadata.name))).size, 'Device records unavailable');
        const issues = placementsDenied ? [] : rackIssues(rack, slots, inventory);
        const used = new Set(slots.flatMap(slot => { const geometry = equipmentGeometry(slot, rack); return geometry ? Array.from({ length: geometry.height }, (_, index) => geometry.position + index) : []; }));
        // Used units only: blank units are never shown as free.
        row.cells.Used = placementsDenied ? unknown('Some placement records are unavailable')
          : !height ? unknown('Height not recorded')
            : issues.some(issue => issue.kind === 'unpositioned') ? unknown('Some equipment has no usable unit position')
              : { text: `${used.size}/${height}U`, numeric: true };
        const feeds = inventory.issues?.PowerFeed ? undefined : inventory.physical.filter(item => item.kind === 'PowerFeed' && item.metadata.namespace === namespace && item.spec.rackRef?.name === rack.metadata.name);
        row.cells.Feeds = count(feeds?.length, 'Power feed records unavailable');
        if (!feeds) row.cells.Power = unknown('Power feed records unavailable');
        else if (!feeds.length) row.cells.Power = NONE;
        else {
          const loads = feeds.map(feedLoad);
          const unitsSeen = new Set(loads.map(load => load.unit));
          row.cells.Power = loads.some(load => !load.measured) || unitsSeen.size > 1 ? unknown('No load reported for every feed')
            : { text: formatPower(loads.reduce((sum, load) => sum + load.measured!.value, 0), loads[0].unit), numeric: true, title: feeds.map((feed, index) => `${label('PowerFeed', feed)}: ${formatPower(loads[index].measured!.value, loads[index].unit)}`).join('\n') };
        }
        for (const issue of issues) flag(issue.kind === 'overlap' ? 'conflict' : 'unavailable', issue.kind === 'overlap' ? issue.text.replace(/\. Flagged, not blocked:$/, '') : `${issue.slots.length} without a usable unit position`);
        if (placementsDenied) flag('unavailable', 'Some placement records are unavailable');
        break;
      }
      case 'Manufacturer': {
        const manufacturer = entry.record as PhysicalRecord;
        const related = manufacturerInventory(manufacturer, inventory);
        row.cells.Aliases = manufacturer.spec.aliases?.length ? { text: manufacturer.spec.aliases.join(', ') } : undefined;
        row.cells['Device types'] = count(inventory.issues?.DeviceType ? undefined : related.models.length, 'Device type records unavailable');
        row.cells.Devices = count(inventory.issues?.Device || inventory.issues?.DeviceType ? undefined : related.devices.length, 'Device or device type records unavailable');
        break;
      }
      case 'DeviceType': {
        const type = entry.record as PhysicalRecord;
        const shape = type.spec.modelShape;
        row.cells.Manufacturer = link('Manufacturer', namespace, type.spec.manufacturerRef?.name);
        row.cells['Model key'] = type.spec.model ? { text: type.spec.model } : NONE;
        row.cells.Height = shape?.heightUnits ? { text: `${shape.heightUnits}U`, numeric: true } : unknown('Height not recorded');
        row.cells.Depth = shape?.fullDepth === undefined ? unknown('Depth not recorded') : { text: shape.fullDepth ? 'Full' : 'Half' };
        row.cells.Ports = { text: String((type.spec.componentTemplates ?? []).filter(item => item.type === 'NetworkPort').length), numeric: true, title: 'Network ports in the template' };
        row.cells.Devices = count(inventory.issues?.Device ? undefined : devices.filter(device => device.metadata.namespace === namespace && device.spec.deviceTypeRef?.name === type.metadata.name).length, 'Device records unavailable');
        break;
      }
    }
    // A cell that needs checking marks its row; the first line of its tooltip is the reason.
    for (const cell of Object.values(row.cells)) if (cell?.attention) flag(cell.attention, cell.title?.split('\n')[0] ?? 'Check this record');
    row.reasons = [...new Set(row.reasons)];
    rows.set(entry, row);
  }
  return rows;
}

/** Plain text of a row, for search. */
export function rowText(row: DirectoryRow) {
  return [...Object.values(row.cells).map(cell => cell?.text), row.note?.text].filter(Boolean).join(' ');
}

