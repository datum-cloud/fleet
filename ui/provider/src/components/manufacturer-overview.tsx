import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@datum-cloud/datum-ui/table';
import { Title } from '@datum-cloud/datum-ui/typography';
import { Link } from 'react-router';
import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory, InventoryKind } from '../inventory';
import { catalogLabel, manufacturerInventory } from '../lib/manufacturer-inventory';
import { inventoryHref } from './inventory-location';
import { RecordTabs, type RecordTab } from './inventory-record-layout';

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
  const devicesUnknown = !!issue('DeviceType') || !!issue('Device');
  const tabs: RecordTab[] = [
    {
      id: 'devices', label: 'Devices', count: devicesUnknown ? '?' : devices.length, content: <section aria-label="Manufacturer devices">
        {issue('DeviceType') ? <p>Device relationships unavailable until device types are readable. Counts are unknown.</p> : issue('Device') ? <p>{unavailable('Device')}</p> : <>
          {unresolved && <p>Some device type references are missing or deleting. Those devices cannot be attributed to a manufacturer; only resolved relationships are shown.</p>}
          {devices.length ? <div className="inventory-scroll" tabIndex={0} role="region" aria-label="Manufacturer devices table"><Table><TableHeader><TableRow><TableHead>Device</TableHead><TableHead>Model</TableHead><TableHead>Reported location</TableHead></TableRow></TableHeader><TableBody>{devices.map(({ device, model }) => <TableRow key={device.metadata.name}><TableCell>{link('Device', device, devices.map(entry => entry.device))}</TableCell><TableCell>{link('DeviceType', model, models)}</TableCell><TableCell>{location(device)}</TableCell></TableRow>)}</TableBody></Table></div> : <p>{unresolved ? 'No resolved device relationships found.' : 'No devices explicitly linked to these device types.'}</p>}
        </>}
      </section>,
    },
  ];
  return <div className="inventory-equipment-sections">
    <section aria-label="Manufacturer device types">
      <Title as="h3" level={5}>Device types <span className="inventory-count">{issue('DeviceType') ? 'unknown' : models.length}</span></Title>
      {issue('DeviceType') ? <p>{unavailable('DeviceType')}</p> : models.length ? <div className="inventory-scroll inventory-fit" tabIndex={0} role="region" aria-label="Manufacturer device types table"><Table><TableHeader><TableRow><TableHead>Device type</TableHead><TableHead>Associated devices</TableHead></TableRow></TableHeader><TableBody>{models.map(model => <TableRow key={model.metadata.name}><TableCell>{link('DeviceType', model, models)}</TableCell><TableCell>{issue('Device') ? 'Unknown' : devices.filter(entry => entry.model.metadata.name === model.metadata.name).length}</TableCell></TableRow>)}</TableBody></Table></div> : <p>No device types linked to this manufacturer.</p>}
    </section>
    <RecordTabs label="Manufacturer details" tabs={tabs} />
  </div>;
}
