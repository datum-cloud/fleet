import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@datum-cloud/datum-ui/table';
import { Link } from 'react-router';
import type { PhysicalRecord } from '../../../shared/fleet-api';
import { modelLabel, slotKey, type Inventory } from '../inventory';
import { devicePlacement, placementFacility } from '../lib/device-placement';
import { displayLabel } from '../lib/display-names';
import { deviceModelLabel } from '../lib/hardware-catalog';
import { siteFacilities } from '../lib/site-membership';
import { DeviceTable, inventoryHref, RacksTable } from './inventory-location';
import { RecordSummary, RecordTabs, SummaryParts, type RecordTab } from './inventory-record-layout';

/** The Site's readable facilities, deduplicated, and the memberships that did not resolve. */
function siteMembers(site: PhysicalRecord, inventory: Inventory) {
  const memberships = siteFacilities({ ...site, spec: { ...site.spec, facilityRefs: [...new Map((site.spec.facilityRefs ?? []).map(ref => [ref.name, ref])).values()] } }, inventory);
  const facilities = [...new Map(memberships.flatMap(item => item.facility ? [[slotKey(item.facility.metadata.namespace, item.facility.metadata.name), item.facility] as const] : [])).values()];
  return { memberships, facilities, unresolved: memberships.filter(item => !item.facility) };
}

const link = (kind: string, record: { metadata: PhysicalRecord['metadata']; spec: { displayName?: string } }) => <Link to={inventoryHref(kind, record.metadata.namespace, record.metadata.name)}>{record.spec.displayName || record.metadata.name}</Link>;

/** The Site header: its code, and its facility when it has exactly one. Otherwise Facilities is a tab. */
export function SiteSummary({ site, inventory }: { site: PhysicalRecord; inventory: Inventory }) {
  const { facilities, unresolved } = siteMembers(site, inventory);
  const single = facilities.length === 1 && !unresolved.length ? facilities[0] : undefined;
  return <RecordSummary><SummaryParts parts={[site.spec.code && <span title="Site code">{site.spec.code}</span>, single && link('Facility', single)]} /></RecordSummary>;
}

// This is a projection of authorized inventory, never a Site assignment or capacity resolver.
export function SiteOverview({ site, inventory }: { site: PhysicalRecord; inventory: Inventory }) {
  const { memberships, facilities, unresolved } = siteMembers(site, inventory);
  const names = new Set(facilities.map(item => item.metadata.name));
  const local = (record: { metadata: PhysicalRecord['metadata'] }) => record.metadata.namespace === site.metadata.namespace && !record.metadata.deletionTimestamp;
  const racks = inventory.issues?.Rack ? [] : [...new Map(inventory.physical.filter(record => record.kind === 'Rack' && local(record) && names.has(record.spec.facilityRef?.name ?? '')).map(record => [record.metadata.name, record])).values()];
  // Exclude deleting racks from both direct and fallback placement resolution.
  const readable = { ...inventory, slots: inventory.slots.filter(slot => !slot.claim.metadata.deletionTimestamp), physical: inventory.physical.filter(record => !record.metadata.deletionTimestamp) };
  const devices = inventory.issues?.Device ? [] : [...new Map([...inventory.slots.flatMap(slot => slot.devices), ...inventory.unboundDevices].filter(local).map(record => [record.metadata.name, record])).values()];
  const locatedDevices = devices.filter(device => {
    const location = devicePlacement(device, readable);
    const facility = location && placementFacility(location.placement, device.metadata.namespace, readable);
    return !!facility && names.has(facility);
  });
  const slots = inventory.issues?.DeviceClaim ? [] : readable.slots.filter(({ claim }) => local(claim) && names.has(placementFacility(claim.spec.placement, claim.metadata.namespace, readable) ?? ''));
  const membershipIncomplete = unresolved.length > 0;
  const resolvedFacility = (name: string | undefined) => !!name && readable.physical.some(record => record.kind === 'Facility' && local(record) && record.metadata.name === name);
  const deviceIncomplete = membershipIncomplete || !!inventory.issues?.Rack || !!inventory.issues?.DeviceClaim || devices.some(device => {
    const location = devicePlacement(device, readable);
    return !location || !resolvedFacility(placementFacility(location.placement, device.metadata.namespace, readable));
  });
  const planIncomplete = membershipIncomplete || !!inventory.issues?.Rack || inventory.slots.some(({ claim }) => local(claim) && !resolvedFacility(placementFacility(claim.spec.placement, claim.metadata.namespace, readable)));
  const denied = (kind: 'Rack' | 'Device' | 'DeviceClaim') => inventory.issues?.[kind]?.status === 403 ? 'Access denied' : 'Records unavailable';
  const mapped = facilities.length > 0;
  const unmapped = 'Unknown until linked facilities are readable';
  const unresolvedNote = membershipIncomplete ? `${inventory.issues?.Facility?.status === 403 ? 'Access denied · ' : ''}${unresolved[0].state}` : undefined;
  // Counts carry their state: "?" when unknown, amber "N+" when incomplete, the reason as the tooltip.
  const count = (kind: 'Rack' | 'Device' | 'DeviceClaim', value: number, incomplete: boolean, why: string): Pick<RecordTab, 'count' | 'countNote'> =>
    !mapped ? { count: '?', countNote: unmapped } : inventory.issues?.[kind] ? { count: '?', countNote: denied(kind) } : incomplete ? { count: `${value}+`, countNote: why } : { count: value };
  const multiple = facilities.length > 1;
  // Plans in rack order, then by unit.
  const plans = slots.map(slot => {
    const { placement } = slot.claim.spec;
    const rack = placement.rackRef && readable.physical.find(record => record.kind === 'Rack' && local(record) && record.metadata.name === placement.rackRef!.name);
    return { slot, rack, placement, rackLabel: rack ? displayLabel(rack) : placement.rackRef?.name ?? '' };
  }).sort((a, b) => a.rackLabel.localeCompare(b.rackLabel) || (a.placement.position ?? 0) - (b.placement.position ?? 0) || displayLabel(a.slot.claim).localeCompare(displayLabel(b.slot.claim)));
  const tabs: RecordTab[] = [];
  if (!(facilities.length === 1 && !membershipIncomplete)) tabs.push({
    id: 'facilities', label: 'Facilities', ...(inventory.issues?.Facility ? { count: '?', countNote: unresolvedNote } : membershipIncomplete ? { count: `${facilities.length}+`, countNote: unresolvedNote } : { count: facilities.length }),
    content: <>
      {facilities.length > 0 && <div className="inventory-scroll"><Table aria-label="Site facilities" className="inventory-dense-table"><TableHeader><TableRow><TableHead>Facility</TableHead><TableHead>Premises code</TableHead><TableHead>Racks</TableHead></TableRow></TableHeader><TableBody>{facilities.map(facility => <TableRow key={facility.metadata.name}>
        <TableCell>{link('Facility', facility)}</TableCell>
        <TableCell title={facility.spec.identifiers?.map(identifier => `${identifier.provider} · ${identifier.type}`).join(' / ')}>{facility.spec.identifiers?.map(identifier => identifier.value).join(' · ') || '—'}</TableCell>
        <TableCell className="inventory-numeric">{inventory.issues?.Rack ? '?' : racks.filter(rack => rack.spec.facilityRef?.name === facility.metadata.name).length}</TableCell>
      </TableRow>)}</TableBody></Table></div>}
      {unresolvedNote && <p data-tone="warning">{unresolvedNote}</p>}
      {!memberships.length && <p className="inventory-muted">No facilities linked.</p>}
    </>,
  });
  tabs.push(
    {
      id: 'racks', label: 'Racks', ...count('Rack', racks.length, membershipIncomplete, unresolvedNote ?? ''),
      content: !mapped ? <p className="inventory-muted">{unmapped}.</p> : inventory.issues?.Rack ? <p>{denied('Rack')}.</p> : racks.length ? <RacksTable racks={racks} inventory={inventory} label="Site racks" /> : <p className="inventory-muted">No racks recorded.</p>,
    },
    {
      id: 'devices', label: 'Devices', ...count('Device', locatedDevices.length, deviceIncomplete, 'Some locations or related records are unresolved or unavailable'),
      content: !mapped ? <p className="inventory-muted">{unmapped}.</p> : inventory.issues?.Device ? <p>{denied('Device')}.</p> : locatedDevices.length ? <DeviceTable devices={locatedDevices} inventory={readable} label="Devices at linked facilities" model={device => deviceModelLabel(device, inventory)} withFacility={multiple} /> : <p className="inventory-muted">No devices recorded.</p>,
    },
    {
      id: 'plans', label: 'Installation plans', ...count('DeviceClaim', plans.length, planIncomplete, 'Some locations or related records are unresolved or unavailable'),
      content: !mapped ? <p className="inventory-muted">{unmapped}.</p> : inventory.issues?.DeviceClaim ? <p>{denied('DeviceClaim')}.</p> : plans.length ? <div className="inventory-scroll"><Table aria-label="Site installation plans" className="inventory-dense-table">
        <TableHeader><TableRow><TableHead>Plan</TableHead><TableHead>Planned location</TableHead><TableHead>Expected model</TableHead><TableHead>Registered device</TableHead></TableRow></TableHeader>
        <TableBody>{plans.map(({ slot, rack, placement }) => {
          const height = placement.height ?? 1;
          const units = placement.mount ? `Mount: ${placement.mount}` : placement.position !== undefined ? `U${placement.position}${height > 1 ? `–U${placement.position + height - 1}` : ''}${placement.face ? ` ${placement.face}` : ''}` : undefined;
          const facility = placementFacility(placement, slot.claim.metadata.namespace, readable);
          const facilityRecord = facilities.find(item => item.metadata.name === facility);
          return <TableRow key={slot.key}>
            <TableCell>{link('DeviceClaim', slot.claim)}</TableCell>
            <TableCell>{[multiple || !placement.rackRef ? (facilityRecord ? displayLabel(facilityRecord) : facility) : undefined, inventory.issues?.Rack ? 'Rack unavailable' : rack ? displayLabel(rack) : placement.rackRef?.name, units].filter(Boolean).join(' · ')}</TableCell>
            <TableCell>{modelLabel(slot.claim.spec.expectedIdentity)}</TableCell>
            <TableCell>{inventory.issues?.Device ? '?' : slot.devices.length ? slot.devices.map((device, index) => <span key={device.metadata.name}>{index > 0 && ', '}{link('Device', device)}</span>) : '—'}</TableCell>
          </TableRow>;
        })}</TableBody>
      </Table></div> : <p className="inventory-muted">No installation plans recorded.</p>,
    },
  );
  return <RecordTabs label="Site details" tabs={tabs} />;
}
