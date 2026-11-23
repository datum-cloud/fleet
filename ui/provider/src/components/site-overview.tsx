import { Title } from '@datum-cloud/datum-ui/typography';
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@datum-cloud/datum-ui/table';
import { Link } from 'react-router';
import type { PhysicalRecord } from '../../../shared/fleet-api';
import { slotKey, type Inventory } from '../inventory';
import { devicePlacement, placementFacility } from '../lib/device-placement';
import { facilityIdentifiers, siteFacilities } from '../lib/site-membership';
import { inventoryHref } from './inventory-location';
import { RecordTabs, type RecordTab } from './inventory-record-layout';

// This is a projection of authorized inventory, never a Site assignment or capacity resolver.
export function SiteOverview({ site, inventory }: { site: PhysicalRecord; inventory: Inventory }) {
  const memberships = siteFacilities({ ...site, spec: { ...site.spec, facilityRefs: [...new Map((site.spec.facilityRefs ?? []).map(ref => [ref.name, ref])).values()] } }, inventory);
  const facilities = [...new Map(memberships.flatMap(item => item.facility ? [[slotKey(item.facility.metadata.namespace, item.facility.metadata.name), item.facility] as const] : [])).values()];
  const unresolved = memberships.filter(item => !item.facility);
  const names = new Set(facilities.map(item => item.metadata.name));
  const local = (record: { metadata: PhysicalRecord['metadata'] }) => record.metadata.namespace === site.metadata.namespace && !record.metadata.deletionTimestamp;
  const racks = inventory.issues?.Rack ? [] : [...new Map(inventory.physical.filter(record => record.kind === 'Rack' && local(record) && names.has(record.spec.facilityRef?.name ?? '')).map(record => [record.metadata.name, record])).values()];
  // Exclude deleting racks from both direct and fallback placement resolution.
  const readable = { ...inventory, slots: inventory.slots.filter(slot => !slot.claim.metadata.deletionTimestamp), physical: inventory.physical.filter(record => !record.metadata.deletionTimestamp) };
  const devices = inventory.issues?.Device ? [] : [...new Map([...inventory.slots.flatMap(slot => slot.devices), ...inventory.unboundDevices].filter(local).map(record => [record.metadata.name, record])).values()];
  const locatedDevices = devices.flatMap(device => {
    const location = devicePlacement(device, readable);
    const facility = location && placementFacility(location.placement, device.metadata.namespace, readable);
    return facility && names.has(facility) ? [{ device, basis: location!.basis }] : [];
  });
  const plans = inventory.issues?.DeviceClaim ? [] : inventory.slots.map(slot => slot.claim).filter(claim => local(claim) && names.has(placementFacility(claim.spec.placement, claim.metadata.namespace, readable) ?? ''));
  const membershipIncomplete = unresolved.length > 0;
  const resolvedFacility = (name: string | undefined) => !!name && readable.physical.some(record => record.kind === 'Facility' && local(record) && record.metadata.name === name);
  const deviceIncomplete = membershipIncomplete || !!inventory.issues?.Rack || !!inventory.issues?.DeviceClaim || devices.some(device => {
    const location = devicePlacement(device, readable);
    return !location || !resolvedFacility(placementFacility(location.placement, device.metadata.namespace, readable));
  });
  const planIncomplete = membershipIncomplete || !!inventory.issues?.Rack || inventory.slots.some(({ claim }) => local(claim) && !resolvedFacility(placementFacility(claim.spec.placement, claim.metadata.namespace, readable)));
  const link = (kind: string, record: { metadata: PhysicalRecord['metadata']; spec: { displayName?: string } }) => <Link to={inventoryHref(kind, record.metadata.namespace, record.metadata.name)}>{record.spec.displayName || record.metadata.name}</Link>;
  const unavailable = (kind: 'Rack' | 'Device' | 'DeviceClaim') => inventory.issues?.[kind]?.status === 403 ? 'Access denied. Request inventory read access, then refresh.' : 'Records unavailable. Refresh inventory to retry.';
  const mapped = facilities.length > 0;
  const count = (value: number, incomplete: boolean) => `${value}${incomplete ? ' found · incomplete' : ' recorded'}`;
  const tabs: RecordTab[] = [
    {
      id: 'racks', label: 'Racks', count: !mapped || inventory.issues?.Rack ? '?' : racks.length, content: !mapped ? <p>Unknown until linked facilities are readable.</p> : <>
        <h4>Racks {inventory.issues?.Rack ? '' : `· ${count(racks.length, membershipIncomplete)}`}</h4>
        {inventory.issues?.Rack ? <p>{unavailable('Rack')}</p> : racks.length ? <ul className="inventory-link-list">{racks.map(rack => <li key={rack.metadata.name}>{link('Rack', rack)}</li>)}</ul> : <p>No matching rack records found. Review facility links and rack records.</p>}
      </>,
    },
    {
      id: 'devices', label: 'Devices', count: !mapped || inventory.issues?.Device ? '?' : locatedDevices.length, content: !mapped ? <p>Unknown until linked facilities are readable.</p> : <>
        <h4>Devices {inventory.issues?.Device ? '' : `· ${count(locatedDevices.length, deviceIncomplete)}`}</h4>
        {inventory.issues?.Device ? <p>{unavailable('Device')}</p> : <>
          {deviceIncomplete && <p>Some locations or related records are unresolved or unavailable; this is not a complete device count.</p>}
          {locatedDevices.length ? <div className="inventory-scroll inventory-fit" tabIndex={0} role="region" aria-label="Devices at linked facilities"><Table><TableHeader><TableRow><TableHead>Device</TableHead><TableHead>Location basis</TableHead></TableRow></TableHeader><TableBody>{locatedDevices.map(({ device, basis }) => <TableRow key={device.metadata.name}><TableCell>{link('Device', device)}</TableCell><TableCell>{basis}</TableCell></TableRow>)}</TableBody></Table></div> : <p>No matching device records found. Review reported locations and claim references.</p>}
        </>}
      </>,
    },
    {
      id: 'plans', label: 'Installation plans', count: !mapped || inventory.issues?.DeviceClaim ? '?' : plans.length, content: !mapped ? <p>Plans cannot be summarized until linked facilities are readable.</p> : inventory.issues?.DeviceClaim ? <p>{unavailable('DeviceClaim')}</p> : <>
        <p>{plans.length} {planIncomplete ? 'found · incomplete' : 'recorded'} · execution status is not recorded by the current API.</p>
        {plans.length ? <ul className="inventory-link-list">{plans.map(plan => <li key={plan.metadata.name}>{link('DeviceClaim', plan)}</li>)}</ul> : <p>No matching installation plans found. Review installation plans and their location references.</p>}
      </>,
    },
  ];
  return <div className="inventory-equipment-sections">
    <section aria-label="Site location">
      <Title as="h3" level={5}>Facilities <span className="inventory-count">{facilities.length}</span></Title>
      {facilities.length > 0 && <ul className="inventory-link-list">{facilities.map(facility => <li key={facility.metadata.name}>
        {link('Facility', facility)}
        <small>{[facilityIdentifiers(facility), facility.spec.locationRef && `Geographic location reference: ${facility.spec.locationRef.name}`].filter(Boolean).join(' · ')}</small>
      </li>)}</ul>}
      {unresolved.length > 0 && <p>{unresolved[0].state}. {inventory.issues?.Facility?.status === 403 ? 'Request Facility read access, then refresh.' : 'Refresh inventory; ask an inventory manager to review unresolved links.'}</p>}
      {!memberships.length && <p>No facilities linked. Ask an inventory manager to record the Site’s facility references.</p>}
      {!mapped && <p>Equipment cannot be summarized until linked facilities are readable.</p>}
    </section>
    <RecordTabs label="Site details" tabs={tabs} />
  </div>;
}
