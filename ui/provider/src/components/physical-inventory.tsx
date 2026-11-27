import { Button } from '@datum-cloud/datum-ui/button';
import { RefreshCw } from 'lucide-react';
import { Title } from '@datum-cloud/datum-ui/typography';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router';
import { InventoryNavigation, sections, useReportInventoryCounts, type InventoryCounts } from './inventory-navigation';
import { isCatalogKind } from './inventory-groups';
import {
  modelLabel,
  placementLabel,
  slotKey,
  type Inventory,
  type RawDevice,
  type RawDeviceClaim,
} from '../inventory';
import type { PhysicalRecord } from '../../../shared/fleet-api';
import './physical-inventory.css';
import { SiteOverview, SiteSummary } from './site-overview';
import { ManufacturerOverview } from './manufacturer-overview';
import { catalogLabel } from '../lib/manufacturer-inventory';
import { InventoryAccessState } from './inventory-access-state';
import { InventoryManagement } from './inventory-management';
import { DevicePlacementEdit } from './device-placement-edit';
import type { ManagedKind, ManagedRecord } from '../lib/inventory-management';
import { InventoryReadAccessContext, readFailureIssues, redactInventory } from '../lib/inventory-read-access';
import { inventoryHref } from './inventory-location';
import { RackDetail, RackSummary } from './rack-detail';
import { devicePlacement, placementFacility, reportedPlacementLabel } from '../lib/device-placement';
import { facilitySites } from '../lib/site-membership';
import { deviceCatalog } from '../lib/hardware-catalog';
import { INVENTORY_NAMESPACE } from '../lib/inventory-namespace';
import { InventoryDirectoryHeading, InventoryToolbar } from './inventory-directory-heading';
import { InventoryPageTitle, inventoryDirectoryPath, inventoryReturnSearch } from './inventory-page-title';
import { directoryColumns, directoryRows, rowText, type Cell } from './inventory-directory-rows';
import { InventoryListTable, type InventoryListColumn } from './inventory-list-table';
import { IssueBanners, RecordFooter, RecordSummary, RecordTabs, SummaryParts } from './inventory-record-layout';
import { cableDetail, claimDetail, deviceDetail, deviceTypeDetail, facilityDetail, powerFeedDetail, type DetailHelpers, type DetailView } from './inventory-record-detail';
import { useTelemetry } from './power-telemetry';

type Kind = 'Site' | 'Device' | 'Facility' | 'Rack' | 'DeviceClaim' | 'Cable' | 'PowerFeed' | 'Manufacturer' | 'DeviceType';
type RecordEntry = { kind: Kind; record: RawDevice | RawDeviceClaim | PhysicalRecord };


export { endpointLabel } from '../inventory';

function recordName(entry: RecordEntry) {
  return entry.record.spec.displayName || entry.record.metadata.name;
}

export function PhysicalInventory({ inventory: source, onRetry, showNavigation = true, scope = 'inventory' }: { inventory: Inventory; onRetry?: () => void; showNavigation?: boolean; scope?: 'inventory' | 'catalog' }) {
  const [readIssues, setReadIssues] = useState<Inventory['issues']>({});
  const telemetry = useTelemetry();
  const inventory = redactInventory(source, readIssues);
  const [params, setParams] = useSearchParams();
  const { pathname } = useLocation();
  const search = params.get('q') ?? '';
  const locationFilter = params.get('location') ?? '';
  const modelFilter = params.get('model') ?? '';
  const setFilter = (key: string, value: string) => setParams(previous => { const next = new URLSearchParams(previous); if (value) next.set(key, value); else next.delete(key); return next; }, { replace: true });
  const setLocationFilter = (value: string) => setFilter('location', value);
  const setModelFilter = (value: string) => setFilter('model', value);
  const heading = useRef<HTMLDivElement>(null);
  const defaultKind = scope === 'catalog' ? 'Manufacturer' : 'Device';
  const section = sections.find((item) => item.kind === params.get('kind')) ?? sections.find(item => item.kind === defaultKind)!;
  // Absolute, so a link reaches the Inventory or Catalog page its kind lives on.
  const href = (kind: string, namespace?: string, name?: string) => {
    const next = new URLSearchParams(inventoryHref(kind, namespace, name));
    if (kind === section.kind) for (const key of ['q', 'location', 'model']) { const value = params.get(key); if (value) next.set(key, value); }
    return `${inventoryDirectoryPath(pathname, kind)}?${next}`;
  };
  const entries: RecordEntry[] = [
    ...inventory.slots.flatMap((slot) => slot.devices.map((record) => ({ kind: 'Device' as const, record }))),
    ...inventory.unboundDevices.map((record) => ({ kind: 'Device' as const, record })),
    ...inventory.slots.map((slot) => ({ kind: 'DeviceClaim' as const, record: slot.claim })),
    ...inventory.physical.map((record) => ({ kind: record.kind, record })),
  ];
  const selectedName = params.get('record');
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
  const rows = directoryRows(records, inventory, telemetry);
  const columns = directoryColumns(section.kind, inventory).filter(column => !column.optional || records.some(entry => rows.get(entry)?.cells[column.head]));
  const searchText = (entry: RecordEntry) => {
    const device = entry.kind === 'Device' ? entry.record as RawDevice : undefined;
    return [recordName(entry), entry.record.metadata.name, rowText(rows.get(entry)!), device?.spec.identity.systemUUID, device?.spec.identity.unidentifiedReason, ...(rows.get(entry)!.cells['A end']?.title?.split('\n') ?? []), ...(rows.get(entry)!.cells['B end']?.title?.split('\n') ?? [])].filter(Boolean).join(' ').toLowerCase();
  };
  const order = (entry: RecordEntry) => rows.get(entry)!.order ?? [];
  const matching = records
    .filter(entry => (!locationFilter || locationKey(entry) === locationFilter) && (!modelFilter || modelKey(entry) === modelFilter))
    .filter((entry) =>
      searchText(entry).includes(search.trim().toLowerCase()),
    )
    .sort((left, right) => {
      const a = order(left);
      const b = order(right);
      for (let index = 0; index < Math.max(a.length, b.length); index++) {
        const difference = typeof a[index] === 'number' && typeof b[index] === 'number' ? (a[index] as number) - (b[index] as number) : String(a[index] ?? '').localeCompare(String(b[index] ?? ''));
        if (difference) return difference;
      }
      return recordName(left).localeCompare(recordName(right)) ||
        slotKey(left.record.metadata.namespace, left.record.metadata.name).localeCompare(slotKey(right.record.metadata.namespace, right.record.metadata.name));
    });
  const filtered = !!(search.trim() || locationFilter || modelFilter);
  const clearFilters = () => setParams(previous => { const next = new URLSearchParams(previous); for (const key of ['q', 'location', 'model']) next.delete(key); return next; }, { replace: true });
  // Counts reach the kind tabs only from here, after the session boundary has resolved.
  const unauthenticated = Object.values(inventory.issues ?? {}).some(issue => issue?.status === 401);
  const counts: InventoryCounts = unauthenticated ? {} : Object.fromEntries(sections.map(item => [item.kind, {
    total: inventory.issues?.[item.kind] ? undefined : entries.filter(entry => entry.kind === item.kind).length,
    shown: item.kind === section.kind && selectedName === null && filtered && !inventory.issues?.[item.kind] ? matching.length : undefined,
  }]));
  useReportInventoryCounts(counts);

  const slotFor = (entry: RecordEntry) => entry.kind === 'Device'
    ? claimFor(entry.record as RawDevice)
    : entry.kind === 'DeviceClaim' ? inventory.slots.find(item => item.key === slotKey(entry.record.metadata.namespace, entry.record.metadata.name)) : undefined;
  const helpers: DetailHelpers = { inventory, entries, href, reference, find, placement, slotFor, telemetry };

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
          summary: <SiteSummary site={site} inventory={inventory} />,
          issues: [], tabs: [],
          primary: <SiteOverview site={site} inventory={inventory} />,
        };
      }
      case 'Manufacturer': {
        const manufacturer = record as PhysicalRecord;
        // An alias that only repeats the record name is already in the footer.
        const aliases = (manufacturer.spec.aliases ?? []).filter(alias => alias !== manufacturer.metadata.name);
        return {
          summary: <RecordSummary><SummaryParts parts={[manufacturer.spec.canonicalName && manufacturer.spec.canonicalName !== recordName(entry) && `Canonical name ${manufacturer.spec.canonicalName}`, aliases.length > 0 && `Also recorded as ${aliases.join(', ')}`]} /></RecordSummary>,
          issues: [], tabs: [],
          primary: <ManufacturerOverview manufacturer={manufacturer} inventory={inventory} />,
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
  // DataTable hands cells copies of the row objects, so rows are found by record key, not identity.
  const entryKey = (entry: RecordEntry) => `${entry.kind}/${slotKey(entry.record.metadata.namespace, entry.record.metadata.name)}`;
  const rowsByKey = new Map([...rows].map(([entry, row]) => [entryKey(entry), row]));
  const rowOf = (entry: RecordEntry) => rowsByKey.get(entryKey(entry))!;
  // The list's columns come from the directory rows: the name links to the
  // record; other cells may link, carry a tooltip, or be marked for attention.
  const cellText = (cell: Cell | undefined) => !cell ? null : cell.text === '?' ? <><span aria-hidden="true">?</span><span className="inventory-visually-hidden">{cell.title ?? 'Unknown'}</span></> : <>{cell.to ? <Link to={cell.to} onClick={event => event.stopPropagation()}>{cell.text}</Link> : cell.text}{cell.attention && cell.title && <span className="inventory-visually-hidden"> · {cell.title.split('\n')[0]}</span>}</>;
  const listColumns: InventoryListColumn<RecordEntry>[] = [
    { id: 'name', header: 'Name', sort: entry => recordName(entry), cell: entry => { const row = rowOf(entry); return <span title={[recordName(entry), ...row.reasons].join('\n')}>
      <Link to={href(entry.kind, entry.record.metadata.namespace, entry.record.metadata.name)} onClick={event => event.stopPropagation()}>{recordName(entry)}</Link>
      {row.note && <span className="inventory-muted" title={row.note.title}> · {row.note.text}</span>}
      {row.reasons.length > 0 && <span className="inventory-visually-hidden"> · {row.reasons.join(' · ')}</span>}
    </span>; } },
    ...columns.map((column): InventoryListColumn<RecordEntry> => ({
      id: column.head, header: column.head,
      sort: entry => { const cell = rowOf(entry).cells[column.head]; if (!cell || cell.text === '?') return ''; const number = Number.parseFloat(cell.text.replace(/^U/, '')); return cell.numeric || /^U\d/.test(cell.text) ? (Number.isNaN(number) ? cell.text : number) : cell.text; },
      cell: entry => { const cell = rowOf(entry).cells[column.head]; return <span className={cell?.numeric ? 'inventory-numeric' : undefined} title={cell?.title ?? (cell && cell.text.length > 24 ? cell.text : undefined)} data-attention={cell?.attention}>{cellText(cell)}</span>; },
    })),
  ];
  if (unauthenticated) return <InventoryAccessState status={401} />;
  const view = selected ? detailView(selected, placementEditor) : undefined;
  const refresh = onRetry && <Button type="secondary" theme="borderless" size="icon" htmlType="button" aria-label="Refresh inventory" title="Refresh inventory" onClick={onRetry} icon={<RefreshCw size={16} aria-hidden="true" />} />;
  const titleActions = <>{refresh}{management}</>;
  return (
    <InventoryReadAccessContext.Provider value={error => setReadIssues(previous => ({ ...previous, ...readFailureIssues(error) }))}>
    <section className="fleet-inventory" aria-label="Physical inventory">
      {showNavigation && <InventoryNavigation counts={counts} scope={isCatalogKind(section.kind) ? 'catalog' : scope} />}
      <div className="inventory-body">
          {inventory.issues?.[section.kind] ? <><InventoryDirectoryHeading section={section} /><InventoryToolbar section={section} onRetry={onRetry} management={management} /><InventoryAccessState status={inventory.issues[section.kind]!.status} onRetry={onRetry} /></> : selectedName !== null ? (
            <>
              <div ref={heading} tabIndex={-1} className="inventory-record-header"><InventoryPageTitle title={selected ? recordName(selected) : 'Record not found'} category={section} directory={href(section.kind)} description={selected?.kind === 'Rack' ? <RackSummary rack={selected.record as PhysicalRecord} inventory={inventory} /> : view?.summary} actions={titleActions} /></div>
              {selected?.kind === 'Rack' ? <RackDetail rack={selected.record as PhysicalRecord} inventory={inventory} /> : selected && view ? (
                <>
                  <IssueBanners issues={view.issues} />
                  <div className="inventory-primary">{view.primary}</div>
                  {view.tabs.length > 0 && <RecordTabs label={`${recordName(selected)} details`} tabs={view.tabs} />}
                  <RecordFooter metadata={selected.record.metadata} extra={view.about} />
                </>
              ) : (
                <p>
                  {selectedNamespace !== INVENTORY_NAMESPACE ? 'This inventory UI only supports the default namespace. Choose a category to browse default inventory.' : `This ${section.title.toLowerCase()} record is not in the loaded inventory. It may have been removed or the link may be out of date.`}
                </p>
              )}
            </>
          ) : (
            <>
              <InventoryDirectoryHeading section={section} />
              <InventoryToolbar key={section.kind} section={section} search={search} onSearch={value => setFilter('q', value)} location={locationFilter} onLocation={inventory.issues?.Facility ? undefined : setLocationFilter} facilities={inventory.physical.filter(record => record.kind === 'Facility').map(record => ({ value: slotKey(record.metadata.namespace, record.metadata.name), label: record.spec.displayName || record.metadata.name }))} model={modelFilter} onModel={setModelFilter} models={filterOptions} onClear={clearFilters} onRetry={onRetry} management={management} />
              {matching.length ? (
                <InventoryListTable key={`table-${section.kind}`} label={section.title} rows={matching} total={records.length} columns={listColumns} resource={section.title.toLowerCase()}
                  rowKey={entry => slotKey(entry.record.metadata.namespace, entry.record.metadata.name)}
                  rowMeta={entry => { const row = rowOf(entry); return { attention: row.attention, title: row.reasons.join('\n') || undefined }; }}
                  onOpen={entry => void navigate(href(entry.kind, entry.record.metadata.namespace, entry.record.metadata.name))}
                  empty="No matching records" />
              ) : (
                <div className="inventory-empty" data-testid="fleet-plugin-inventory-empty">
                  <section.icon size={32} aria-hidden="true" />
                  <Title as="h3" level={5}>
                    {records.length ? 'No matching records' : `No ${section.title.toLowerCase()} recorded`}
                  </Title>
                  {filtered && (
                    <Button type="secondary" theme="outline" size="small" htmlType="button" onClick={clearFilters}>
                      Clear filters
                    </Button>
                  )}
                </div>
              )}
            </>
          )}
      </div>
    </section>
    </InventoryReadAccessContext.Provider>
  );
}
