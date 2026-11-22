import { Button } from '@datum-cloud/datum-ui/button';
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell, TableCaption } from '@datum-cloud/datum-ui/table';
import { Title } from '@datum-cloud/datum-ui/typography';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Link, useLocation, useSearchParams } from 'react-router';
import { InventoryNavigation, sections } from './inventory-navigation';
import {
  modelLabel,
  placementLabel,
  legacyPeerLabel,
  slotKey,
  type Inventory,
  type RawDevice,
  type RawDeviceClaim,
} from '../inventory';
import type { CableEndpoint, PhysicalComponent, PhysicalRecord } from '../../../shared/fleet-api';
import './physical-inventory.css';
import { SiteOverview } from './site-overview';
import { ManufacturerOverview } from './manufacturer-overview';
import { catalogLabel } from '../lib/manufacturer-inventory';
import { InventoryAccessState } from './inventory-access-state';
import { InventoryManagement } from './inventory-management';
import { DevicePlacementEdit } from './device-placement-edit';
import type { ManagedKind, ManagedRecord } from '../lib/inventory-management';
import { InventoryReadAccessContext, readFailureIssues, redactInventory } from '../lib/inventory-read-access';
import { FacilityOverview, RackOverview, inventoryHref } from './inventory-location';
import { devicePlacement, placementFacility, reportedPlacementLabel } from '../lib/device-placement';
import { facilityIdentifiers, facilitySites } from '../lib/site-membership';
import { deviceCatalog, deviceModelLabel } from '../lib/hardware-catalog';
import { INVENTORY_NAMESPACE } from '../lib/inventory-namespace';
import { InventoryDirectoryHeading, InventoryToolbar } from './inventory-directory-heading';
import { InventoryPageTitle, inventoryDirectoryPath, inventoryReturnSearch } from './inventory-page-title';

type Kind = 'Site' | 'Device' | 'Facility' | 'Rack' | 'DeviceClaim' | 'Cable' | 'PowerFeed' | 'Manufacturer' | 'DeviceType';
type RecordEntry = { kind: Kind; record: RawDevice | RawDeviceClaim | PhysicalRecord };


export { endpointLabel } from '../inventory';

function recordName(entry: RecordEntry) {
  return entry.record.spec.displayName || entry.record.metadata.name;
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{children ?? 'Not recorded'}</dd>
    </div>
  );
}

function Components({ components }: { components: PhysicalComponent[] }) {
  return components.length ? (
    <div className="inventory-scroll">
      <Table>
        <TableCaption>Physical components</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead>Name / ID</TableHead>
            <TableHead>Type</TableHead>
            <TableHead>Connector / positions</TableHead>
            <TableHead>Identity</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {components.map((component) => (
            <TableRow key={component.id}>
              <TableCell>
                {component.name}
                <small>{component.id}</small>
              </TableCell>
              <TableCell>{component.type}</TableCell>
              <TableCell>
                {component.connector ?? 'Not recorded'} / {component.positions ?? 'Not recorded'}
              </TableCell>
              <TableCell>
                {component.serial ?? 'No serial recorded'}
                <small>
                  {component.assetTag ? `Asset tag: ${component.assetTag}` : 'No asset tag recorded'}
                </small>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  ) : (
    <p className="inventory-muted">No components recorded.</p>
  );
}

export function PhysicalInventory({ inventory: source, onRetry, showNavigation = true }: { inventory: Inventory; onRetry?: () => void; showNavigation?: boolean }) {
  const [readIssues, setReadIssues] = useState<Inventory['issues']>({});
  const inventory = redactInventory(source, readIssues);
  const [params, setParams] = useSearchParams();
  const { pathname } = useLocation();
  const search = params.get('q') ?? '';
  const [searchDraft, setSearchDraft] = useState(search);
  const locationFilter = params.get('location') ?? '';
  const modelFilter = params.get('model') ?? '';
  const setFilter = (key: string, value: string) => setParams(previous => { const next = new URLSearchParams(previous); if (value) next.set(key, value); else next.delete(key); return next; }, { replace: true });
  const setLocationFilter = (value: string) => setFilter('location', value);
  const setModelFilter = (value: string) => setFilter('model', value);
  const heading = useRef<HTMLDivElement>(null);
  const section = sections.find((item) => item.kind === params.get('kind')) ?? sections[0];
  const href = (kind: string, namespace?: string, name?: string) => {
    const next = new URLSearchParams(inventoryHref(kind, namespace, name));
    if (kind === section.kind) for (const key of ['q', 'location', 'model']) { const value = params.get(key); if (value) next.set(key, value); }
    return `?${next}`;
  };
  const entries: RecordEntry[] = [
    ...inventory.slots.flatMap((slot) => slot.devices.map((record) => ({ kind: 'Device' as const, record }))),
    ...inventory.unboundDevices.map((record) => ({ kind: 'Device' as const, record })),
    ...inventory.slots.map((slot) => ({ kind: 'DeviceClaim' as const, record: slot.claim })),
    ...inventory.physical.map((record) => ({ kind: record.kind, record })),
  ];
  const selectedName = params.get('record');
  useEffect(() => { setSearchDraft(search); }, [search, section.kind, selectedName]);
  const selectedNamespace = params.get('namespace') ?? INVENTORY_NAMESPACE;
  const selected = entries.find(
    (entry) =>
      entry.kind === section.kind &&
      entry.record.metadata.name === selectedName &&
      (entry.record.metadata.namespace ?? '') === selectedNamespace,
  );
  useEffect(() => {
    if (selectedName !== null) heading.current?.focus();
  }, [section.kind, selectedName, selectedNamespace]);
  const find = (kind: Kind, namespace: string | undefined, name: string) =>
    entries.find(
      (entry) =>
        entry.kind === kind &&
        slotKey(entry.record.metadata.namespace, entry.record.metadata.name) === slotKey(namespace, name),
    );
  const reference = (kind: Kind, namespace: string | undefined, name: string) => {
    if (inventory.issues?.[kind]) return <span>Related record unavailable</span>;
    const target = find(kind, namespace, name);
    return target ? (
      <Link to={href(kind, namespace, name)}>{kind === 'Manufacturer' || kind === 'DeviceType' ? catalogLabel(target.record, entries.filter(entry => entry.kind === kind && entry.record.metadata.namespace === namespace).map(entry => entry.record)) : recordName(target)}</Link>
    ) : (
      <span>
        {name} <small>(record not found)</small>
      </span>
    );
  };
  const claimFor = (device: RawDevice) =>
    inventory.slots.find(
      (slot) => slot.key === slotKey(device.metadata.namespace, device.spec.claimRef.name),
    );
  const plannedPlacement = (claim: RawDeviceClaim) => {
    const location = claim.spec.placement;
    if (!location.scope) return placementLabel(claim, inventory.issues);
    const rack = location.rackRef ? find('Rack', claim.metadata.namespace, location.rackRef.name) : undefined;
    const facilityName =
      location.facilityRef?.name ?? (rack?.record as PhysicalRecord | undefined)?.spec.facilityRef?.name;
    const facility = facilityName ? find('Facility', claim.metadata.namespace, facilityName) : undefined;
    const parts = [
      inventory.issues?.Facility ? 'Facility unavailable' : facility ? recordName(facility) : facilityName,
      inventory.issues?.Rack ? 'Rack unavailable' : rack ? recordName(rack) : location.rackRef?.name,
    ];
    if (location.scope === 'Facility') parts.push('Rack not recorded');
    if (location.scope === 'RackMembership') parts.push('Position not recorded');
    if (location.scope === 'NonRackUnitMount') parts.push(`Mount: ${location.mount ?? 'Not recorded'}`);
    if (location.scope === 'RackUnits')
      parts.push(
        location.position === undefined
          ? 'Position not recorded'
          : `U${location.position}${(location.height ?? 1) > 1 ? `–U${location.position + (location.height ?? 1) - 1}` : ''} · ${location.face ?? 'Face not recorded'}`,
      );
    return parts.filter(Boolean).join(' · ');
  };
  const placement = (entry: RecordEntry): string => {
    if (entry.kind === 'DeviceClaim') return plannedPlacement(entry.record as RawDeviceClaim);
    if (entry.kind === 'Device') {
      const projected = devicePlacement(entry.record as RawDevice, inventory);
      if (projected?.basis === 'Reported placement') return reportedPlacementLabel(projected, inventory);
      if (inventory.issues?.DeviceClaim) return 'Claim details unavailable';
      return projected?.claim
        ? `Claim-derived fallback · ${plannedPlacement(projected.claim)}${projected.multipleRegistrations ? ' · multiple registrations · active device unknown' : ''}`
        : 'Claim record not found';
    }
    const record = entry.record as PhysicalRecord;
    if (entry.kind === 'Site') return 'Operational membership only';
    if (entry.kind === 'Facility') return facilitySites(record, inventory);
    if (record.spec.rackRef) return inventory.issues?.Rack ? 'Rack unavailable' : record.spec.rackRef.name;
    if (record.spec.facilityRef) return inventory.issues?.Facility ? 'Facility unavailable' : record.spec.facilityRef.name;
    return 'Not recorded';
  };
  const summary = (entry: RecordEntry): string => {
    if (entry.kind === 'Device') return deviceModelLabel(entry.record as RawDevice, inventory);
    if (entry.kind === 'DeviceClaim')
      return modelLabel((entry.record as RawDeviceClaim).spec.expectedIdentity);
    const record = entry.record as PhysicalRecord;
    if (entry.kind === 'Site') return `Operational Site · ${record.spec.code}`;
    if (entry.kind === 'Manufacturer') return [record.spec.canonicalName, ...(record.spec.aliases ?? [])].join(' · ');
    if (entry.kind === 'DeviceType') return `Model key: ${record.spec.model} · reference template`;
    if (entry.kind === 'Facility') return facilityIdentifiers(record);
    if (entry.kind === 'Rack')
      return record.spec.heightUnits ? `${record.spec.heightUnits}U rack` : 'Height not recorded';
    if (entry.kind === 'Cable')
      return `${record.spec.medium ?? 'Medium not recorded'} · ${record.spec.lanes?.length ?? 0} recorded lanes`;
    if (entry.kind === 'PowerFeed')
      return record.spec.supply
        ? `${record.spec.supply.type} · ${record.spec.supply.voltageV ?? 'Unknown'} V · ${record.spec.supply.currentA ?? 'Unknown'} A`
        : 'Supply not recorded';
    return 'Physical premises';
  };
  const records = entries.filter((entry) => entry.kind === section.kind);
  const locationKey = (entry: RecordEntry) => {
    if (entry.kind === 'Device') {
      const projected = devicePlacement(entry.record as RawDevice, inventory);
      const facility = projected && placementFacility(projected.placement, entry.record.metadata.namespace, inventory);
      return facility ? slotKey(entry.record.metadata.namespace, facility) : '';
    }
    if (inventory.issues?.Facility) return '';
    const claim = entry.kind === 'DeviceClaim' ? entry.record as RawDeviceClaim : undefined;
    const rackName = claim?.spec.placement.rackRef?.name ?? (entry.record as PhysicalRecord).spec.rackRef?.name;
    const rack = rackName && !inventory.issues?.Rack ? find('Rack', entry.record.metadata.namespace, rackName)?.record as PhysicalRecord | undefined : undefined;
    const facilityName = claim?.spec.placement.facilityRef?.name ?? (entry.record as PhysicalRecord).spec.facilityRef?.name ?? rack?.spec.facilityRef?.name;
    return facilityName ? slotKey(entry.record.metadata.namespace, facilityName) : '';
  };
  const modelKey = (entry: RecordEntry) => entry.kind === 'Device' ? deviceCatalog(entry.record as RawDevice, inventory)?.model.spec.model ?? (entry.record as RawDevice).spec.identity.model : entry.kind === 'Cable' ? (entry.record as PhysicalRecord).spec.medium ?? '' : '';
  const filterOptions = [...new Set(records.map(modelKey).filter(Boolean))].sort();
  const searchText = (entry: RecordEntry) => {
    const device = entry.kind === 'Device' ? entry.record as RawDevice : undefined;
    const physical = entry.record as PhysicalRecord;
    const endpoints = entry.kind === 'Cable' ? physical.spec.lanes?.flatMap(lane => [lane.a, lane.b]).flatMap(endpoint => {
      const kind = endpoint.deviceRef ? 'Device' : 'PowerFeed';
      if (inventory.issues?.[kind]) return [];
      const name = endpoint.deviceRef?.name ?? endpoint.powerFeedRef?.name ?? '';
      const target = find(kind, entry.record.metadata.namespace, name);
      return [name, target ? recordName(target) : '', endpoint.componentID];
    }) : [];
    return [recordName(entry), entry.record.metadata.name, entry.record.metadata.namespace, summary(entry), placement(entry), device?.spec.identity.serial, device?.spec.identity.systemUUID, device?.spec.identity.unidentifiedReason, device?.spec.assetTag, ...(endpoints ?? [])].filter(Boolean).join(' ').toLowerCase();
  };
  const matching = records
    .filter(entry => (!locationFilter || locationKey(entry) === locationFilter) && (!modelFilter || modelKey(entry) === modelFilter))
    .filter((entry) =>
      searchText(entry).includes(search.trim().toLowerCase()),
    )
    .sort(
      (left, right) =>
        recordName(left).localeCompare(recordName(right)) ||
        slotKey(left.record.metadata.namespace, left.record.metadata.name).localeCompare(
          slotKey(right.record.metadata.namespace, right.record.metadata.name),
        ),
    );

  function Endpoint({ endpoint, namespace }: { endpoint: CableEndpoint; namespace?: string }) {
    if ((endpoint.deviceRef && inventory.issues?.Device) || (endpoint.powerFeedRef && inventory.issues?.PowerFeed)) return <span>Related endpoint unavailable</span>;
    return (
      <span>
        {endpoint.deviceRef
          ? reference('Device', namespace, endpoint.deviceRef.name)
          : endpoint.powerFeedRef
            ? reference('PowerFeed', namespace, endpoint.powerFeedRef.name)
            : 'Endpoint not recorded'}
        <small>
          {endpoint.componentID} · position {endpoint.position}
        </small>
      </span>
    );
  }

  function Detail({ entry }: { entry: RecordEntry }) {
    const { record, kind } = entry;
    if (kind === 'Site') return <SiteOverview site={record as PhysicalRecord} inventory={inventory} />;
    if (kind === 'Manufacturer') return <ManufacturerOverview manufacturer={record as PhysicalRecord} inventory={inventory} />;
    const namespace = record.metadata.namespace;
    const device = kind === 'Device' ? (record as RawDevice) : undefined;
    const catalog = device && deviceCatalog(device, inventory);
    const claim = kind === 'DeviceClaim' ? (record as RawDeviceClaim) : undefined;
    const physical = !device && !claim ? (record as PhysicalRecord) : undefined;
    const slot = device
      ? claimFor(device)
      : claim
        ? inventory.slots.find((item) => item.key === slotKey(namespace, claim.metadata.name))
        : undefined;
    const sameNamespace = (candidate: RecordEntry) =>
      (candidate.record.metadata.namespace ?? '') === (namespace ?? '');
    const related = entries.filter((candidate) => {
      if (!sameNamespace(candidate)) return false;
      if (candidate.kind === 'DeviceClaim') {
        if (kind === 'Cable')
          return (candidate.record as RawDeviceClaim).spec.cableRefs?.some(
            (cable) => cable.name === record.metadata.name,
          );
        const location = (candidate.record as RawDeviceClaim).spec.placement;
        const rack = location.rackRef
          ? (find('Rack', namespace, location.rackRef.name)?.record as PhysicalRecord | undefined)
          : undefined;
        return kind === 'Rack'
          ? location.rackRef?.name === record.metadata.name
          : kind === 'Facility' &&
              (location.facilityRef?.name === record.metadata.name ||
                rack?.spec.facilityRef?.name === record.metadata.name);
      }
      if (candidate.kind === 'Device')
        return (
          kind === 'DeviceClaim' &&
          (candidate.record as RawDevice).spec.claimRef.name === record.metadata.name
        );
      const target = candidate.record as PhysicalRecord;
      if (target.kind === 'Cable')
        return target.spec.lanes?.some((lane) =>
          [lane.a, lane.b].some((endpoint) => {
            if ((kind === 'Rack' || kind === 'Facility') && endpoint.deviceRef) {
              const targetDevice = find('Device', namespace, endpoint.deviceRef.name)?.record as RawDevice | undefined;
              const location = targetDevice && devicePlacement(targetDevice, inventory)?.placement;
              const rack = location?.rackRef && find('Rack', namespace, location.rackRef.name)?.record as PhysicalRecord | undefined;
              return kind === 'Rack' ? location?.rackRef?.name === record.metadata.name : location?.facilityRef?.name === record.metadata.name || rack?.spec.facilityRef?.name === record.metadata.name;
            }
            return kind === 'Device'
              ? endpoint.deviceRef?.name === record.metadata.name
              : kind === 'PowerFeed' && endpoint.powerFeedRef?.name === record.metadata.name;
          }),
        );
      return kind === 'Facility'
        ? target.spec.facilityRef?.name === record.metadata.name
        : kind === 'Rack' && target.spec.rackRef?.name === record.metadata.name;
    });
    const relatedRecords = kind === 'Facility' || kind === 'Rack' ? related.filter(item => item.kind === 'Cable' || item.kind === 'PowerFeed') : related;
    return (
      <>
        {kind === 'Facility' && <FacilityOverview facility={record as PhysicalRecord} inventory={inventory} />}
        {kind === 'Rack' && <RackOverview rack={record as PhysicalRecord} inventory={inventory} />}
        <dl className="inventory-fields">
          {kind === 'DeviceType' && <>
            <Field label="Manufacturer">{physical?.spec.manufacturerRef && reference('Manufacturer', namespace, physical.spec.manufacturerRef.name)}</Field>
            <Field label="Normalized model identity">{physical?.spec.model}</Field>
            <Field label="Nominal shape">{physical?.spec.modelShape ? `${physical.spec.modelShape.heightUnits ?? 'Unknown'}U · ${physical.spec.modelShape.fullDepth === undefined ? 'Depth unknown' : physical.spec.modelShape.fullDepth ? 'Full depth' : 'Partial depth'}` : 'Unknown'}</Field>
            <Field label="Component templates">{physical?.spec.componentTemplates?.map(template => <div key={template.id}>{template.name} · {template.type} · {template.connector ?? 'Connector unknown'} · {template.positions ?? 1} position(s)</div>) ?? 'Not recorded'}</Field>
            <Field label="Template semantics">Reference defaults only. No installed assets, measured geometry, observed occupancy, or automatic materialization.</Field>
          </>}
          {kind === 'Facility' && <>
            <Field label="Provider premises identifiers">{facilityIdentifiers(record as PhysicalRecord)}</Field>
            <Field label="Operational sites">{facilitySites(record as PhysicalRecord, inventory)}</Field>
          </>}
          <Field label="Record name">{record.metadata.name}</Field>
          <Field label="Namespace">{namespace || 'Not recorded'}</Field>
          {device && (
            <>
              <Field label="Manufacturer / model">{deviceModelLabel(device, inventory)}</Field>
              <Field label="Recorded hardware observation">{modelLabel(device.spec.identity)}</Field>
              <Field label="Shared catalog">{catalog ? reference('DeviceType', namespace, catalog.model.metadata.name) : device.spec.deviceTypeRef ? 'Catalog unavailable · showing recorded observation' : 'Not assigned · showing recorded observation'}</Field>
              <Field label="Catalog nominal height">{catalog?.model.spec.modelShape?.heightUnits === undefined ? 'Unknown' : `${catalog.model.spec.modelShape.heightUnits}U · not measured or used for occupancy`}</Field>
              <Field label="Lifecycle">{device.spec.lifecycle ?? 'Active'}</Field>
              <Field label="Serial number">{device.spec.identity.serial}</Field>
              <Field label="Asset tag">{device.spec.assetTag}</Field>
              <Field label="System UUID">{device.spec.identity.systemUUID}</Field>
              {device.spec.identity.unidentified && <Field label="Hardware identity">No serial or system UUID recorded · {device.spec.identity.unidentifiedReason} · cannot back machine capacity</Field>}
              <Field label="Claim / registration history">
                {reference('DeviceClaim', namespace, device.spec.claimRef.name)}
              </Field>
              <Field label="Location / Reported placement">{placement(entry)}</Field>
              {device.spec.reportedPlacement?.rackRef && <Field label="Reported rack">{reference('Rack', namespace, device.spec.reportedPlacement.rackRef.name)}</Field>}
              {device.spec.reportedPlacement?.facilityRef && <Field label="Reported facility">{reference('Facility', namespace, device.spec.reportedPlacement.facilityRef.name)}</Field>}
              {device.spec.reportedPlacement && <Field label="Placement report">Revision {device.spec.reportedPlacement.revision} · {device.spec.reportedPlacement.observedAt}, not physically verified · Reason: {device.spec.reportedPlacement.reason} · Evidence: {device.spec.reportedPlacement.reference}</Field>}
              <Field label="Placement corrections">Recordkeeping is not a physical move or a capacity reservation.</Field>
              <Field label="Recorded nominal dimensions">
                {device.spec.modelShape
                  ? `${device.spec.modelShape.heightUnits ?? 'Unknown'}U · ${device.spec.modelShape.fullDepth === undefined ? 'Depth not recorded' : device.spec.modelShape.fullDepth ? 'Full depth' : 'Partial depth'}`
                  : undefined}
              </Field>
              <Field label="Management endpoint">{device.spec.bmc?.endpoint}</Field>
              <Field label="Provider host reference">
                {device.spec.providerHostRef &&
                  `${device.spec.providerHostRef.system}/${device.spec.providerHostRef.type}/${device.spec.providerHostRef.id}`}
              </Field>
            </>
          )}
          {claim && (
            <>
              <Field label="Expected hardware">{modelLabel(claim.spec.expectedIdentity)}</Field>
              <Field label="Expected serial">{claim.spec.expectedIdentity.serial}</Field>
              <Field label="Claim placement (intent / history)">{plannedPlacement(claim)}</Field>
              <Field label="Registered devices">{inventory.issues?.Device ? 'Device details unavailable' : slot?.devices.length ?? 0}</Field>
              {claim.spec.placement.facilityRef && (
                <Field label="Facility">
                  {reference('Facility', namespace, claim.spec.placement.facilityRef.name)}
                </Field>
              )}
              {claim.spec.placement.rackRef && (
                <Field label="Rack">{reference('Rack', namespace, claim.spec.placement.rackRef.name)}</Field>
              )}
            </>
          )}
          {physical?.spec.facilityRef && (
            <Field label="Facility">{reference('Facility', namespace, physical.spec.facilityRef.name)}</Field>
          )}
          {physical?.spec.rackRef && (
            <Field label="Rack">{reference('Rack', namespace, physical.spec.rackRef.name)}</Field>
          )}
          {physical?.spec.locationRef && (
            <Field label="Geographic location reference">{physical.spec.locationRef.name}</Field>
          )}
          {kind === 'Rack' && (
            <>
              <Field label="Rack height">
                {physical?.spec.heightUnits === undefined ? 'Not recorded' : `${physical.spec.heightUnits}U`}
              </Field>
              <Field label="Free space">Unknown — recorded placement is not verified occupancy.</Field>
            </>
          )}
          {kind === 'Cable' && <Field label="Medium">{physical?.spec.medium}</Field>}
          {kind === 'PowerFeed' && (
            <>
              <Field label="Supply">{summary(entry)}</Field>
              <Field label="Phase">{physical?.spec.supply?.phase}</Field>
              <Field label="Usable budget / redundancy">
                Unknown — supply ratings do not establish available power.
              </Field>
            </>
          )}
        </dl>
        {slot && slot.devices.length > 1 && (
          <p className="inventory-note">
            Multiple devices reference this claim. All registrations are retained; the active device and its
            placement are unknown.
          </p>
        )}
        {device && (
          <>
            <Title as="h3" level={5}>Components & connections</Title>
            <Components components={device.spec.components ?? []} />
            {!!device.spec.internalMappings?.length && (
              <>
                <h4>Internal paths</h4>
                <ul>
                  {device.spec.internalMappings.map((mapping, index) => (
                    <li key={index}>
                      {mapping.from.componentID}:{mapping.from.position} → {mapping.to.componentID}:
                      {mapping.to.position}
                    </li>
                  ))}
                </ul>
              </>
            )}
            {!!device.spec.serviceAssociations?.length && (
              <>
                <h4>Service references</h4>
                <ul>
                  {device.spec.serviceAssociations.map((association, index) => (
                    <li key={index}>
                      {association.componentID} → {association.serviceRef.system}/
                      {association.serviceRef.type}/{association.serviceRef.id}
                    </li>
                  ))}
                </ul>
              </>
            )}
            <details>
              <summary>Recorded network interfaces & legacy peers</summary>
              <p className="inventory-muted">
                Legacy peers are retained as history. Cable records describe typed physical connections;
                neither verifies a live link.
              </p>
              <ul>
                {device.spec.interfaces?.map((iface) => (
                  <li key={iface.name}>
                    {iface.name} · {iface.macAddress ?? 'MAC not recorded'}
                    {iface.peer && ` · legacy peer ${legacyPeerLabel(iface.peer, inventory.issues)}`}
                  </li>
                ))}
              </ul>
              <h4>Unmapped MAC addresses</h4>
              <p>{device.spec.unmappedMACAddresses?.join(', ') || 'None recorded'}</p>
            </details>
          </>
        )}
        {claim && (
          <>
            <Title as="h3" level={5}>Planned connections</Title>
            <ul>
              {claim.spec.cableRefs?.map((cable) => (
                <li key={cable.name}>{reference('Cable', namespace, cable.name)}</li>
              ))}
            </ul>
            {!claim.spec.cableRefs?.length && (
              <p className="inventory-muted">No cable references recorded.</p>
            )}
            <details>
              <summary>Legacy expected ports</summary>
              <ul>
                {claim.spec.ports?.map((port) => (
                  <li key={port.name}>
                    {port.name} · {port.type ?? 'Type not recorded'} · {port.speedMbps ?? 'Unknown'} Mb/s
                    {port.expectedPeer && ` → ${legacyPeerLabel(port.expectedPeer, inventory.issues)}`}
                  </li>
                ))}
              </ul>
              {!claim.spec.ports?.length && <p>No expected ports recorded.</p>}
            </details>
          </>
        )}
        {kind === 'Cable' && (
          <>
            <Title as="h3" level={5}>Cable lanes</Title>
            {physical?.spec.lanes?.length ? (
              <div className="inventory-scroll">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Lane</TableHead>
                      <TableHead>End A</TableHead>
                      <TableHead>End B</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {physical.spec.lanes.map((lane) => (
                      <TableRow key={lane.id}>
                        <TableCell>{lane.id}</TableCell>
                        <TableCell>
                          <Endpoint endpoint={lane.a} namespace={namespace} />
                        </TableCell>
                        <TableCell>
                          <Endpoint endpoint={lane.b} namespace={namespace} />
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            ) : (
              <p>No lanes recorded.</p>
            )}
          </>
        )}
        {kind === 'PowerFeed' && (
          <>
            <Title as="h3" level={5}>Terminations</Title>
            <Components components={physical?.spec.terminations ?? []} />
            <Title as="h3" level={5}>Upstream sources</Title>
            <ul>
              {physical?.spec.upstreamSources?.map((source, index) => (
                <li key={index}>
                  {source.system}/{source.type}/{source.id}
                </li>
              ))}
            </ul>
            {!physical?.spec.upstreamSources?.length && (
              <p className="inventory-muted">No upstream sources recorded.</p>
            )}
          </>
        )}
        {(kind !== 'Facility' && kind !== 'Rack' || relatedRecords.length > 0) && <><Title as="h3" level={5}>
          Related records
        </Title>
        {relatedRecords.length ? (
          <ul className="inventory-related">
            {relatedRecords.map((item) => (
              <li key={`${item.kind}/${slotKey(namespace, item.record.metadata.name)}`}>
                <span className="inventory-muted">
                  {sections.find((section) => section.kind === item.kind)?.title}
                </span>
                <Link to={href(item.kind, namespace, item.record.metadata.name)}>{recordName(item)}</Link>
                <small>{placement(item)}</small>
              </li>
            ))}
          </ul>
        ) : (
          <p className="inventory-muted">
            No related records found in this namespace. This does not establish that equipment or connections
            are absent.
          </p>
        )}</>}
        <details>
          <summary>Record history & verification</summary>
          <dl className="inventory-fields">
            <Field label="Verification">Unknown — no verification evidence is available.</Field>
            <Field label="Created">{record.metadata.creationTimestamp}</Field>
          </dl>
        </details>
      </>
    );
  }

  const management =
    (['Facility', 'Rack', 'Device', 'Cable'].includes(section.kind) || (section.kind === 'DeviceClaim' && selectedName === null)) &&
    (selectedName === null || selected) ? <InventoryManagement
      compact
      key={`${section.kind}/${selectedNamespace}/${selectedName ?? ''}`}
      kind={section.kind as ManagedKind}
      createHref={selectedName === null && ['Device', 'Cable', 'DeviceClaim'].includes(section.kind) ? `${inventoryDirectoryPath(pathname)}/create/${section.kind === 'DeviceClaim' ? 'installation-plan' : section.kind.toLowerCase()}${inventoryReturnSearch(params.toString(), section.kind)}` : undefined}
      issues={inventory.issues}
      record={selected ? { ...selected.record, kind: selected.kind } as ManagedRecord : undefined}
      facilities={inventory.physical.filter(record => record.kind === 'Facility')}
      racks={inventory.physical.filter(record => record.kind === 'Rack')}
      claims={inventory.slots.map(slot => slot.claim)}
      devices={entries.filter(entry => entry.kind === 'Device').map(entry => entry.record as RawDevice)}
      feeds={inventory.physical.filter(record => record.kind === 'PowerFeed')}
      catalog={inventory.physical.filter(record => record.kind === 'Manufacturer' || record.kind === 'DeviceType')}
    /> : null;
  const placementEditor = selected?.kind === 'Device' && !inventory.issues?.Device ? <DevicePlacementEdit key={`${selectedNamespace}/${selectedName}`} device={selected.record as RawDevice} locations={inventory.physical} issues={inventory.issues} /> : null;
  if (Object.values(inventory.issues ?? {}).some(issue => issue?.status === 401)) return <InventoryAccessState status={401} />;
  return (
    <InventoryReadAccessContext.Provider value={error => setReadIssues(previous => ({ ...previous, ...readFailureIssues(error) }))}>
    <section className="fleet-inventory" aria-label="Physical inventory">
      {showNavigation && <InventoryNavigation inventory={inventory} />}
      <div className="inventory-body">
          {inventory.issues?.[section.kind] ? <><InventoryDirectoryHeading section={section} onRetry={onRetry} management={management} /><InventoryToolbar section={section} /><InventoryAccessState status={inventory.issues[section.kind]!.status} onRetry={onRetry} /></> : selectedName !== null ? (
            <>
              <div className="inventory-list-heading"><Link to={href(section.kind)}>← Back to {section.title.toLowerCase()}</Link>{onRetry && <Button type="secondary" theme="outline" size="small" htmlType="button" onClick={onRetry}>Refresh inventory</Button>}</div>
              <div ref={heading} tabIndex={-1}><InventoryPageTitle title={selected ? recordName(selected) : 'Record not found'} category={section} directory={href(section.kind)} actions={management} /></div>
              {placementEditor}
              {selected ? (
                <>
                  {selected.kind !== 'Site' && selected.kind !== 'Manufacturer' && <p className="inventory-muted">
                    {section.title} /{' '}
                    {selected.record.metadata.name}
                  </p>}
                  <Detail entry={selected} />
                </>
              ) : (
                <p>
                  {selectedNamespace !== INVENTORY_NAMESPACE ? 'This inventory UI only supports the default namespace. Choose a category to browse default inventory.' : `This ${section.title.toLowerCase()} record is not in the loaded inventory. It may have been removed or the link may be out of date.`}
                </p>
              )}
            </>
          ) : (
            <>
              <InventoryDirectoryHeading section={section} onRetry={onRetry} management={management} count={`${matching.length} of ${records.length} records`} />
              <InventoryToolbar section={section} search={searchDraft} onSearch={setSearchDraft} onApplySearch={() => setFilter('q', searchDraft)} location={locationFilter} onLocation={inventory.issues?.Facility ? undefined : setLocationFilter} facilities={inventory.physical.filter(record => record.kind === 'Facility').map(record => ({ value: slotKey(record.metadata.namespace, record.metadata.name), label: record.spec.displayName || record.metadata.name }))} model={modelFilter} onModel={setModelFilter} models={filterOptions} onClear={() => setParams(previous => { const next = new URLSearchParams(previous); next.delete('location'); next.delete('model'); return next; }, { replace: true })} />
              {matching.length ? (
                <div className="inventory-scroll" tabIndex={0} role="region" aria-label={`${section.title} table scroll area`}>
                  <Table aria-label={section.title} data-testid="fleet-plugin-inventory-table">
                    <TableHeader>
                      <TableRow>
                        <TableHead>Name</TableHead>
                        <TableHead>
                          {section.kind === 'Device' || section.kind === 'DeviceClaim'
                            ? 'Hardware'
                            : 'Details'}
                        </TableHead>
                        {section.kind !== 'Manufacturer' && <TableHead>
                          {section.kind === 'Cable' ? 'End A' : section.kind === 'Device' || section.kind === 'DeviceClaim'
                            ? 'Location / Reported placement'
                            : 'Location / reference'}
                        </TableHead>}
                        {(section.kind === 'Cable' || section.kind === 'Device') && <TableHead>{section.kind === 'Cable' ? 'End B' : 'Serial / asset tag'}</TableHead>}
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {matching.map((entry) => (
                        <TableRow key={slotKey(entry.record.metadata.namespace, entry.record.metadata.name)}>
                          <TableCell>
                            <Link
                              to={href(
                                entry.kind,
                                entry.record.metadata.namespace,
                                entry.record.metadata.name,
                              )}
                            >
                              {recordName(entry)}
                            </Link>
                            {entry.record.spec.displayName && (entry.kind !== 'Manufacturer' && entry.kind !== 'DeviceType' || catalogLabel(entry.record, records.map(item => item.record)) !== recordName(entry)) && <small>{entry.record.metadata.name}</small>}
                          </TableCell>
                          <TableCell>{summary(entry)}</TableCell>
                          {entry.kind !== 'Manufacturer' && <TableCell>{entry.kind === 'Cable' ? (entry.record as PhysicalRecord).spec.lanes?.map(lane => <div key={lane.id}><Endpoint endpoint={lane.a} namespace={entry.record.metadata.namespace} /></div>) : entry.kind === 'Rack' && (entry.record as PhysicalRecord).spec.facilityRef ? reference('Facility', entry.record.metadata.namespace, (entry.record as PhysicalRecord).spec.facilityRef!.name) : placement(entry)}</TableCell>}
                          {(entry.kind === 'Cable' || entry.kind === 'Device') && <TableCell>
                            {entry.kind === 'Cable' ? (entry.record as PhysicalRecord).spec.lanes?.map(lane => <div key={lane.id}><Endpoint endpoint={lane.b} namespace={entry.record.metadata.namespace} /></div>) : entry.kind === 'Device' ? (
                              <>
                                {(entry.record as RawDevice).spec.identity.serial ?? ((entry.record as RawDevice).spec.identity.unidentified ? 'No hardware identity' : 'Serial not recorded')}
                                <small>
                                  {(entry.record as RawDevice).spec.assetTag ?? 'Asset tag not recorded'}
                                </small>
                              </>
                            ) : (
                              entry.record.metadata.namespace || 'Not recorded'
                            )}
                          </TableCell>}
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              ) : (
                <div className="inventory-empty" data-testid="fleet-plugin-inventory-empty">
                  <section.icon size={32} aria-hidden="true" />
                  <Title as="h3" level={5}>
                    {records.length ? 'No matching records' : `No ${section.title.toLowerCase()} recorded`}
                  </Title>
                  <p>
                    {records.length
                      ? 'Try another name, serial number, or location.'
                      : `This inventory has no ${section.title.toLowerCase()} records yet. Other categories may still contain hardware or plans.`}
                  </p>
                  {search && (
                    <Button type="secondary" theme="outline" size="small" htmlType="button" onClick={() => setFilter('q', '')}>
                      Clear search
                    </Button>
                  )}
                </div>
              )}
            </>
          )}
      </div>
      <details className="inventory-help">
        <summary>About this inventory</summary>
        <p>
          These are reported records and registration / intent history, not live hardware status. A record does not
          establish availability, verified placement, working connections, or permission to operate equipment.
        </p>
        <p>
          Viewing and management permissions are separate. Authorized inventory managers can create facilities,
          racks, installation plans with unknown positions, device registrations, and cables here, and edit display names of records created by this portal. Other records require an
          explicit ownership review. Nothing here synchronizes external systems or provisions hardware.
        </p>
        <p>
          Claims retain every registered device. Multiple registrations do not identify which device is
          active. Missing values mean not recorded, not absent.
        </p>
      </details>
    </section>
    </InventoryReadAccessContext.Provider>
  );
}
