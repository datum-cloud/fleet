import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@datum-cloud/datum-ui/table';
import { Link } from 'react-router';
import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory, InventoryKind } from '../inventory';
import { catalogLabel, manufacturerInventory } from '../lib/manufacturer-inventory';
import { inventoryHref } from './inventory-location';
import { RecordTabs, type RecordTab } from './inventory-record-layout';

/** "1U · full", "2U", "Zero-U": the catalog size, without depth for a zero-U strip. */
function sizeLabel(model: PhysicalRecord) {
  const shape = model.spec.modelShape;
  if (shape?.heightUnits === 0) return 'Zero-U';
  return [shape?.heightUnits === undefined ? '?' : `${shape.heightUnits}U`, shape?.fullDepth === undefined ? undefined : shape.fullDepth ? 'full' : 'half'].filter(Boolean).join(' · ');
}

export function ManufacturerOverview({ manufacturer, inventory }: { manufacturer: PhysicalRecord; inventory: Inventory }) {
  const { models, devices, unresolved } = manufacturerInventory(manufacturer, inventory);
  const issue = (kind: InventoryKind) => inventory.issues?.[kind];
  const unavailable = (kind: InventoryKind) => issue(kind)?.status === 403 ? 'Access denied.' : 'Records unavailable.';
  const link = (kind: string, record: PhysicalRecord | Device, peers: (PhysicalRecord | Device)[]) => <Link to={inventoryHref(kind, record.metadata.namespace, record.metadata.name)}>{catalogLabel(record, peers)}</Link>;
  // The facility is named only when the devices span more than one, or a device has no rack.
  const facilities = new Set(devices.flatMap(({ device }) => device.spec.reportedPlacement?.facilityRef ? [device.spec.reportedPlacement.facilityRef.name] : []));
  // Reported placement only: a claim's planned location is never substituted here.
  const location = (device: Device) => {
    const placement = device.spec.reportedPlacement;
    if (!placement) return 'Not recorded';
    const reference = (kind: 'Facility' | 'Rack', name: string) => {
      if (issue(kind)) return <span>{kind} {issue(kind)?.status === 403 ? 'access denied' : 'unavailable'}</span>;
      const peers = inventory.physical.filter(record => record.kind === kind && record.metadata.namespace === device.metadata.namespace && !record.metadata.deletionTimestamp);
      const target = peers.find(record => record.metadata.name === name);
      return target ? link(kind, target, peers) : <span>{kind} reference missing</span>;
    };
    const facility = placement.facilityRef && (facilities.size > 1 || !placement.rackRef);
    return <>{facility && reference('Facility', placement.facilityRef!.name)}{facility && placement.rackRef && ' · '}{placement.rackRef && reference('Rack', placement.rackRef.name)}{placement.position !== undefined && ` · U${placement.position}`}{placement.mount && ` · Mount: ${placement.mount}`}{placement.face && ` · ${placement.face}`}</>;
  };
  const deleting = !!manufacturer.metadata.deletionTimestamp;
  const devicesUnknown = deleting || !!issue('DeviceType') || !!issue('Device');
  const serials = devices.some(({ device }) => device.spec.identity.serial);
  const tags = devices.some(({ device }) => device.spec.assetTag);
  const deletingNote = 'Manufacturer is deleting; catalog relationships are unknown';
  const unresolvedNote = 'Some device type references are missing or deleting; those devices are not attributed';
  const tabs: RecordTab[] = [
    {
      id: 'types', label: 'Device types', count: deleting || issue('DeviceType') ? '?' : models.length, countNote: deleting ? deletingNote : undefined, content: <>
        {deleting ? <p>Unknown while the manufacturer is deleting.</p> : issue('DeviceType') ? <p>{unavailable('DeviceType')}</p> : models.length ? <div className="inventory-scroll" tabIndex={0} role="region" aria-label="Manufacturer device types table"><Table className="inventory-dense-table"><TableHeader><TableRow><TableHead>Device type</TableHead><TableHead>Model key</TableHead><TableHead>Size</TableHead><TableHead>Devices</TableHead></TableRow></TableHeader><TableBody>{models.map(model => <TableRow key={model.metadata.name}><TableCell>{link('DeviceType', model, models)}</TableCell><TableCell>{model.spec.model ?? '—'}</TableCell><TableCell>{sizeLabel(model)}</TableCell><TableCell className="inventory-numeric">{issue('Device') ? '?' : devices.filter(entry => entry.model.metadata.name === model.metadata.name).length}</TableCell></TableRow>)}</TableBody></Table></div> : <p className="inventory-muted">No device types linked to this manufacturer.</p>}
      </>,
    },
    {
      id: 'devices', label: 'Devices', count: devicesUnknown ? '?' : unresolved ? `${devices.length}+` : devices.length, countNote: deleting ? deletingNote : unresolved && !devicesUnknown ? unresolvedNote : undefined, content: <>
        {deleting ? <p>Unknown while the manufacturer is deleting.</p> : issue('DeviceType') ? <p>Unknown until device types are readable.</p> : issue('Device') ? <p>{unavailable('Device')}</p> : devices.length ? <div className="inventory-scroll" tabIndex={0} role="region" aria-label="Manufacturer devices table"><Table className="inventory-dense-table">
          <TableHeader><TableRow><TableHead>Device</TableHead><TableHead>Model</TableHead><TableHead>Rack · U</TableHead>{serials && <TableHead>Serial</TableHead>}{tags && <TableHead>Asset tag</TableHead>}</TableRow></TableHeader>
          <TableBody>{devices.map(({ device, model }) => <TableRow key={device.metadata.name}>
            <TableCell>{link('Device', device, devices.map(entry => entry.device))}</TableCell>
            <TableCell>{link('DeviceType', model, models)}</TableCell>
            <TableCell>{location(device)}</TableCell>
            {serials && <TableCell className="inventory-numeric">{device.spec.identity.serial}</TableCell>}
            {tags && <TableCell className="inventory-numeric">{device.spec.assetTag}</TableCell>}
          </TableRow>)}</TableBody>
        </Table></div> : <p className="inventory-muted">{unresolved ? 'No resolved devices.' : 'No devices of these types.'}</p>}
      </>,
    },
  ];
  return <RecordTabs label="Manufacturer details" tabs={tabs} />;
}
