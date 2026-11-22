import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@datum-cloud/datum-ui/table';
import { Title } from '@datum-cloud/datum-ui/typography';
import { Link } from 'react-router';
import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory, InventoryKind } from '../inventory';
import { catalogLabel, manufacturerInventory } from '../lib/manufacturer-inventory';
import { inventoryHref } from './inventory-location';

export function ManufacturerOverview({ manufacturer, inventory }: { manufacturer: PhysicalRecord; inventory: Inventory }) {
  const { models, devices, unresolved } = manufacturerInventory(manufacturer, inventory);
  if (manufacturer.metadata.deletionTimestamp) return <p>Manufacturer is deleting; catalog relationships are unavailable, not empty.</p>;
  const issue = (kind: InventoryKind) => inventory.issues?.[kind];
  const unavailable = (kind: InventoryKind) => issue(kind)?.status === 403 ? 'Access denied. Request inventory read access, then refresh.' : 'Records unavailable. Refresh inventory to retry; counts are unknown.';
  const link = (kind: string, record: PhysicalRecord | Device, peers: (PhysicalRecord | Device)[]) => <Link to={inventoryHref(kind, record.metadata.namespace, record.metadata.name)}>{catalogLabel(record, peers)}</Link>;
  const location = (device: Device) => {
    const placement = device.spec.reportedPlacement;
    if (!placement) return 'Not recorded';
    const reference = (kind: 'Facility' | 'Rack', name: string) => {
      if (issue(kind)) return <span>{kind} {issue(kind)?.status === 403 ? 'access denied' : 'unavailable'}</span>;
      const peers = inventory.physical.filter(record => record.kind === kind && record.metadata.namespace === device.metadata.namespace && !record.metadata.deletionTimestamp);
      const target = peers.find(record => record.metadata.name === name);
      return target ? link(kind, target, peers) : <span>{kind} reference missing</span>;
    };
    return <>{placement.facilityRef && reference('Facility', placement.facilityRef.name)}{placement.facilityRef && placement.rackRef && ' · '}{placement.rackRef && reference('Rack', placement.rackRef.name)}{placement.position !== undefined && ` · U${placement.position}`}{placement.mount && ` · Mount: ${placement.mount}`}{placement.face && ` · ${placement.face}`}</>;
  };
  return <div className="inventory-equipment-sections">
    <section aria-label="Manufacturer overview">
      <Title as="h3" level={5}>Overview</Title>
      <dl className="inventory-fields">
        <div><dt>Manufacturer</dt><dd>{catalogLabel(manufacturer)}</dd></div>
        <div><dt>Canonical name</dt><dd>{manufacturer.spec.canonicalName || 'Not recorded'}</dd></div>
        <div><dt>Descriptive aliases</dt><dd>{manufacturer.spec.aliases?.join(' · ') || 'Not recorded'}</dd></div>
      </dl>
      <p className="inventory-muted">Relationships use explicit catalog references in the default namespace, not recorded manufacturer text. Counts describe linked inventory records, not readiness or available capacity.</p>
    </section>
    <section aria-label="Manufacturer device types">
      <Title as="h3" level={5}>Device types</Title>
      {issue('DeviceType') ? <p>{unavailable('DeviceType')}</p> : models.length ? <div className="inventory-scroll" tabIndex={0} role="region" aria-label="Manufacturer device types table"><Table><TableHeader><TableRow><TableHead>Device type</TableHead><TableHead>Associated devices</TableHead></TableRow></TableHeader><TableBody>{models.map(model => <TableRow key={model.metadata.name}><TableCell>{link('DeviceType', model, models)}</TableCell><TableCell>{issue('Device') ? 'Unknown' : devices.filter(entry => entry.model.metadata.name === model.metadata.name).length}</TableCell></TableRow>)}</TableBody></Table></div> : <p>No device types linked to this manufacturer.</p>}
    </section>
    <section aria-label="Manufacturer devices">
      <Title as="h3" level={5}>Devices</Title>
      {issue('DeviceType') ? <p>Device relationships unavailable until device types are readable. Counts are unknown.</p> : issue('Device') ? <p>{unavailable('Device')}</p> : <>
        {unresolved && <p>Some device type references are missing or deleting. Those devices cannot be attributed to a manufacturer; only resolved relationships are shown.</p>}
        {devices.length ? <div className="inventory-scroll" tabIndex={0} role="region" aria-label="Manufacturer devices table"><Table><TableHeader><TableRow><TableHead>Device</TableHead><TableHead>Model</TableHead><TableHead>Reported location</TableHead></TableRow></TableHeader><TableBody>{devices.map(({ device, model }) => <TableRow key={device.metadata.name}><TableCell>{link('Device', device, devices.map(entry => entry.device))}</TableCell><TableCell>{link('DeviceType', model, models)}</TableCell><TableCell>{location(device)}</TableCell></TableRow>)}</TableBody></Table></div> : <p>{unresolved ? 'No resolved device relationships found.' : 'No devices explicitly linked to these device types.'}</p>}
        <p className="inventory-muted">Locations come only from each device’s reported placement, never installation plans or catalog templates. Missing location references do not remove a device from the count.</p>
      </>}
    </section>
    <details><summary>Technical details</summary><dl className="inventory-fields">
      <div><dt>Record name</dt><dd>{manufacturer.metadata.name}</dd></div>
      <div><dt>Namespace</dt><dd>{manufacturer.metadata.namespace}</dd></div>
      <div><dt>Resource UID</dt><dd>{manufacturer.metadata.uid || 'Not recorded'}</dd></div>
      <div><dt>Created</dt><dd>{manufacturer.metadata.creationTimestamp || 'Not recorded'}</dd></div>
    </dl><p>References follow namespace/name. Recreating a name does not verify physical continuity.</p></details>
  </div>;
}
