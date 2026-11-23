import { Button } from '@datum-cloud/datum-ui/button';
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@datum-cloud/datum-ui/table';
import { Title } from '@datum-cloud/datum-ui/typography';
import { useEffect, useRef, useState, type MouseEvent, type ReactNode } from 'react';
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router';
import { InventoryNavigation, sections } from './inventory-navigation';
import {
  modelLabel,
  placementLabel,
  slotKey,
  type Inventory,
  type RawDevice,
  type RawDeviceClaim,
} from '../inventory';
import type { CableEndpoint, PhysicalRecord } from '../../../shared/fleet-api';
import './physical-inventory.css';
import { SiteOverview } from './site-overview';
import { ManufacturerOverview } from './manufacturer-overview';
import { catalogLabel } from '../lib/manufacturer-inventory';
import { InventoryAccessState } from './inventory-access-state';
import { InventoryManagement } from './inventory-management';
import { DevicePlacementEdit } from './device-placement-edit';
import type { ManagedKind, ManagedRecord } from '../lib/inventory-management';
import { InventoryReadAccessContext, readFailureIssues, redactInventory } from '../lib/inventory-read-access';
import { inventoryHref } from './inventory-location';
import { RackDetail } from './rack-detail';
import { devicePlacement, placementFacility, reportedPlacementLabel } from '../lib/device-placement';
import { facilityIdentifiers, facilitySites } from '../lib/site-membership';
import { deviceCatalog, deviceModelLabel } from '../lib/hardware-catalog';
import { INVENTORY_NAMESPACE } from '../lib/inventory-namespace';
import { InventoryDirectoryHeading, InventoryToolbar } from './inventory-directory-heading';
import { InventoryPageTitle, inventoryDirectoryPath, inventoryReturnSearch } from './inventory-page-title';
import { AboutRecord, IssueBanners, RecordSummary, RecordTabs, SummaryParts } from './inventory-record-layout';
import { cableDetail, claimDetail, deviceDetail, deviceTypeDetail, facilityDetail, powerFeedDetail, type DetailHelpers, type DetailView } from './inventory-record-detail';
import { sharedValue } from '../lib/display-names';

type Kind = 'Site' | 'Device' | 'Facility' | 'Rack' | 'DeviceClaim' | 'Cable' | 'PowerFeed' | 'Manufacturer' | 'DeviceType';
type RecordEntry = { kind: Kind; record: RawDevice | RawDeviceClaim | PhysicalRecord };


export { endpointLabel } from '../inventory';

function recordName(entry: RecordEntry) {
  return entry.record.spec.displayName || entry.record.metadata.name;
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


  /** A Cable end in a list row: the target named once, then its ports. */
  const endSummary = (cable: PhysicalRecord, side: 'a' | 'b') => {
    const lanes = cable.spec.lanes ?? [];
    const label = (endpoint: CableEndpoint) => {
      const kind = endpoint.deviceRef ? 'Device' : endpoint.powerFeedRef ? 'PowerFeed' : undefined;
      if (!kind) return 'Endpoint not recorded';
      if (inventory.issues?.[kind]) return 'Related endpoint unavailable';
      const name = endpoint.deviceRef?.name ?? endpoint.powerFeedRef!.name;
      const target = find(kind, cable.metadata.namespace, name);
      return target ? recordName(target) : `${name} (record not found)`;
    };
    if (!lanes.length) return 'No lanes recorded';
    const target = sharedValue(lanes.map(lane => label(lane[side])));
    const ports = [...new Set(lanes.map(lane => lane[side].componentID))];
    if (target) return <>{target}<small>{ports.join(', ')}{lanes.length > 1 ? ` · ${lanes.length} lanes` : ''}</small></>;
    return lanes.map(lane => <div key={lane.id}>{label(lane[side])}<small>{lane[side].componentID}</small></div>);
  };

  /** A Device's location in a list row: rack and units; facility only when there is more than one. */
  const multipleFacilities = inventory.physical.filter(record => record.kind === 'Facility').length > 1;
  const listLocation = (device: RawDevice): string => {
    const projected = devicePlacement(device, inventory);
    if (!projected) return inventory.issues?.DeviceClaim ? 'Claim details unavailable' : claimFor(device) ? 'Location unknown' : 'Claim record not found';
    const { placement: where } = projected;
    const display = (kind: 'Facility' | 'Rack', name?: string) => name && (inventory.issues?.[kind] ? `${kind} unavailable` : find(kind, device.metadata.namespace, name) ? recordName(find(kind, device.metadata.namespace, name)!) : name);
    const facility = placementFacility(where, device.metadata.namespace, inventory);
    const units = where.mount ? `mount: ${where.mount}` : where.position !== undefined ? `U${where.position}${(where.height ?? 1) > 1 ? `–U${where.position + (where.height ?? 1) - 1}` : ''}` : where.rackRef ? 'position unknown' : 'rack unknown';
    const parts = [multipleFacilities || !where.rackRef ? display('Facility', facility) ?? 'Facility unknown' : undefined, display('Rack', where.rackRef?.name), units];
    return `${parts.filter(Boolean).join(' · ')}${projected.basis === 'Claim-derived fallback' ? ' (planned, claim-derived)' : ''}${projected.multipleRegistrations ? ' · active device unknown' : ''}`;
  };

  const slotFor = (entry: RecordEntry) => entry.kind === 'Device'
    ? claimFor(entry.record as RawDevice)
    : entry.kind === 'DeviceClaim' ? inventory.slots.find(item => item.key === slotKey(entry.record.metadata.namespace, entry.record.metadata.name)) : undefined;
  const helpers: DetailHelpers = { inventory, entries, href, reference, find, placement, slotFor };

  function detailView(entry: RecordEntry, locationEditor: ReactNode): DetailView {
    const record = entry.record;
    switch (entry.kind) {
      case 'Device': return deviceDetail(record as RawDevice, helpers, locationEditor);
      case 'DeviceClaim': return claimDetail(record as RawDeviceClaim, helpers);
      case 'Cable': return cableDetail(record as PhysicalRecord, helpers);
      case 'PowerFeed': return powerFeedDetail(record as PhysicalRecord, helpers);
      case 'Facility': return facilityDetail(record as PhysicalRecord, helpers);
      case 'DeviceType': return deviceTypeDetail(record as PhysicalRecord, helpers);
      // The rack page (rack-detail.tsx) renders its own header summary, tabs and About.
      case 'Rack': return { summary: undefined, issues: [], primary: null, tabs: [] };
      case 'Site': {
        const site = record as PhysicalRecord;
        return {
          summary: <RecordSummary><SummaryParts parts={[site.spec.code && `Site code ${site.spec.code}`, 'Operational membership only']} /></RecordSummary>,
          issues: [], tabs: [],
          primary: <SiteOverview site={site} inventory={inventory} />,
          about: [{ label: 'Internal Site code', value: site.spec.code }],
        };
      }
      case 'Manufacturer': {
        const manufacturer = record as PhysicalRecord;
        const aliases = manufacturer.spec.aliases ?? [];
        return {
          summary: <RecordSummary><SummaryParts parts={[manufacturer.spec.canonicalName && manufacturer.spec.canonicalName !== recordName(entry) && `Canonical name ${manufacturer.spec.canonicalName}`, aliases.length > 0 && `Also recorded as ${aliases.join(', ')}`]} /></RecordSummary>,
          issues: [], tabs: [],
          primary: <ManufacturerOverview manufacturer={manufacturer} inventory={inventory} />,
          about: [{ label: 'Canonical name', value: manufacturer.spec.canonicalName }, { label: 'Descriptive aliases', value: aliases.join(' · ') }],
        };
      }
    }
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
  const navigate = useNavigate();
  const openRow = (event: MouseEvent<HTMLElement>, target: string) => {
    if ((event.target as HTMLElement).closest('a,button,input,select,summary')) return;
    if (window.getSelection()?.toString()) return;
    void navigate(target);
  };
  if (Object.values(inventory.issues ?? {}).some(issue => issue?.status === 401)) return <InventoryAccessState status={401} />;
  const view = selected ? detailView(selected, placementEditor) : undefined;
  const refresh = onRetry && <Button type="secondary" theme="outline" size="small" htmlType="button" onClick={onRetry}>Refresh inventory</Button>;
  const titleActions = <>{refresh}{management}</>;
  const kindColumns = section.kind === 'Device' ? ['Model', 'Location', 'Serial number']
    : section.kind === 'Cable' ? ['Medium', 'End A', 'End B']
      : section.kind === 'Rack' ? ['Facility', 'Height']
        : section.kind === 'Manufacturer' ? ['Details']
          : ['Details', section.kind === 'DeviceClaim' ? 'Planned location' : 'Location / reference'];
  const cells = (entry: RecordEntry): ReactNode[] => {
    const physical = entry.record as PhysicalRecord;
    const namespace = entry.record.metadata.namespace;
    if (entry.kind === 'Device') {
      const device = entry.record as RawDevice;
      const catalog = deviceCatalog(device, inventory);
      return [catalog ? recordName({ kind: 'DeviceType', record: catalog.model }) : modelLabel(device.spec.identity), listLocation(device), device.spec.identity.serial ?? (device.spec.identity.unidentified ? 'No hardware identity' : 'Not recorded')];
    }
    if (entry.kind === 'Cable') return [physical.spec.medium ?? 'Not recorded', endSummary(physical, 'a'), endSummary(physical, 'b')];
    if (entry.kind === 'Rack') return [physical.spec.facilityRef ? reference('Facility', namespace, physical.spec.facilityRef.name) : 'Not recorded', physical.spec.heightUnits ? `${physical.spec.heightUnits}U` : 'Not recorded'];
    if (entry.kind === 'Manufacturer') return [summary(entry)];
    return [summary(entry), placement(entry)];
  };
  return (
    <InventoryReadAccessContext.Provider value={error => setReadIssues(previous => ({ ...previous, ...readFailureIssues(error) }))}>
    <section className="fleet-inventory" aria-label="Physical inventory">
      {showNavigation && <InventoryNavigation inventory={inventory} />}
      <div className="inventory-body">
          {inventory.issues?.[section.kind] ? <><InventoryDirectoryHeading section={section} onRetry={onRetry} management={management} /><InventoryToolbar section={section} /><InventoryAccessState status={inventory.issues[section.kind]!.status} onRetry={onRetry} /></> : selectedName !== null ? (
            <>
              <div ref={heading} tabIndex={-1} className="inventory-record-header"><InventoryPageTitle title={selected ? recordName(selected) : 'Record not found'} category={section} directory={href(section.kind)} description={view?.summary} actions={selected?.kind === 'Rack' ? refresh : titleActions} /></div>
              {selected?.kind === 'Rack' ? <RackDetail rack={selected.record as PhysicalRecord} inventory={inventory} management={management} /> : selected && view ? (
                <>
                  <IssueBanners issues={view.issues} />
                  <div className="inventory-primary">{view.primary}</div>
                  {view.tabs.length > 0 && <RecordTabs label={`${recordName(selected)} details`} tabs={view.tabs} />}
                  <AboutRecord metadata={selected.record.metadata} extra={view.about} />
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
                        {kindColumns.map(column => <TableHead key={column}>{column}</TableHead>)}
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {matching.map((entry) => {
                        const target = href(entry.kind, entry.record.metadata.namespace, entry.record.metadata.name);
                        return <TableRow key={slotKey(entry.record.metadata.namespace, entry.record.metadata.name)} className="inventory-row-link" onClick={event => openRow(event, target)}>
                          <TableCell>
                            <Link to={target}>{recordName(entry)}</Link>
                            {(entry.kind === 'Manufacturer' || entry.kind === 'DeviceType') && entry.record.spec.displayName && catalogLabel(entry.record, records.map(item => item.record)) !== recordName(entry) && <small>{entry.record.metadata.name}</small>}
                          </TableCell>
                          {cells(entry).map((cell, index) => <TableCell key={index}>{cell}</TableCell>)}
                        </TableRow>;
                      })}
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
