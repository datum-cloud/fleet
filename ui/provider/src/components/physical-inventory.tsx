import type { Inventory } from '../inventory';
import type { CableEndpoint } from '../../../shared/fleet-api';
import { useState } from 'react';

export function endpointLabel(endpoint: CableEndpoint): string {
  return `${endpoint.deviceRef?.name ?? endpoint.powerFeedRef?.name ?? 'Unknown'}/${endpoint.componentID}:${endpoint.position}`;
}

export function PhysicalInventory({ inventory }: { inventory: Inventory }) {
  const [report, setReport] = useState<{ source: string; classification: string; reason: string }[]>([]);
  const [error, setError] = useState('');
  const [summary, setSummary] = useState<{ complete: boolean; errors: string[]; missing: string[] }>();
  const [search, setSearch] = useState('');
  const matching = report.filter((row) => `${row.source} ${row.classification} ${row.reason}`.toLowerCase().includes(search.toLowerCase()));
  return <section aria-label="Physical inventory" className="flex flex-col gap-4">
    <h2>Physical records · verification Unknown</h2>
    <p>Observational records do not reserve space, establish an active binding, or authorize hardware operations. Cable is the typed topology authority; legacy inline peers remain unmigrated history.</p>
    <p>Operators enter and review records in the private inventory Git source. Flux is the staging writer. NetBox and import reports are not required; this page never applies or synchronizes inventory.</p>
    <label>Optional: inspect a private review report locally (not uploaded)
      <input type="file" accept="application/json,.json" onChange={async (event) => {
        setReport([]); setError(''); setSummary(undefined);
        const file = event.target.files?.[0];
        if (!file) return;
        try {
          if (file.size > 10 * 1024 * 1024) throw new Error('Report exceeds 10 MiB');
          const parsed: unknown = JSON.parse(await file.text());
          if (!parsed || typeof parsed !== 'object' || !('version' in parsed) || parsed.version !== 1 || !('records' in parsed) || !Array.isArray(parsed.records) || !parsed.records.every((row: unknown) => row && typeof row === 'object' && 'source' in row && typeof row.source === 'string' && 'classification' in row && typeof row.classification === 'string' && 'reason' in row && typeof row.reason === 'string')) throw new Error('Invalid Fleet import report');
          setReport(parsed.records);
          const fields = parsed as Record<string, unknown>;
          const collectionErrors = fields.collectionErrors && typeof fields.collectionErrors === 'object' ? Object.entries(fields.collectionErrors).map(([key, reason]) => `${key}: ${typeof reason === 'string' ? reason : 'Unknown'}`) : [];
          const missing = [fields.missingSincePrevious, fields.retainedMissingSource].flatMap((items) => Array.isArray(items) ? items.filter((item): item is string => typeof item === 'string') : []);
          setSummary({ complete: fields.coverageComplete === true, errors: collectionErrors, missing });
        } catch { setError('Could not read report. Use a Fleet version 1 report below 10 MiB.'); }
      }} />
    </label>
    {error && <p role="alert">{error}</p>}
    {summary && <div><h3>Optional source comparison — candidates, not applied records</h3>
      <p>Collection fetch: {summary.complete ? 'Complete (not physical parity)' : 'Incomplete or Unknown'} · {report.length} classified records</p>
      <ul>{summary.errors.map((message) => <li key={message}>{message}</li>)}</ul>
      <p>Missing source records are retained, never deleted: {summary.missing.length}</p>
      <ul>{summary.missing.map((identity, index) => <li key={`${identity}/${index}`}>{identity}</li>)}</ul>
      <label>Filter review report <input value={search} onChange={(event) => setSearch(event.target.value)} /></label>
      <p>Showing {Math.min(matching.length, 250)} of {matching.length} matching rows</p>
      <ul>{matching.slice(0, 250).map((row) => <li key={row.source}>{row.source}: {row.classification} — {row.reason}</li>)}</ul>
    </div>}
    {inventory.physical.map((record) => <details key={`${record.kind}/${record.metadata.namespace}/${record.metadata.name}`} id={`physical-${record.kind}-${record.metadata.namespace}-${record.metadata.name}`}>
      <summary>{record.kind}: {record.spec.displayName} ({record.metadata.name})</summary>
      <p>Namespace: {record.metadata.namespace} · Source: {record.spec.provenance.method} · Verification: Unknown</p>
      {record.spec.provenance.source && <p>Source object: {record.spec.provenance.source.system}/{record.spec.provenance.source.type}/{record.spec.provenance.source.id} · Mapping: {record.spec.provenance.source.mappingRevision}</p>}
      {record.spec.facilityRef && <p>Facility: <a href={`#physical-Facility-${record.metadata.namespace}-${record.spec.facilityRef.name}`}>{record.spec.facilityRef.name}</a></p>}
      {record.spec.rackRef && <p>Rack: <a href={`#physical-Rack-${record.metadata.namespace}-${record.spec.rackRef.name}`}>{record.spec.rackRef.name}</a></p>}
      {record.spec.locationRef && <p>Geographic location reference: {record.spec.locationRef.name}</p>}
      {record.kind === 'Rack' && <p>Height: {record.spec.heightUnits ?? 'Unknown'} · Free space: Unknown</p>}
      {record.spec.lanes?.map((lane) => <p key={lane.id}>{lane.id}: {endpointLabel(lane.a)} ↔ {endpointLabel(lane.b)} ({record.spec.medium})</p>)}
      {record.kind === 'PowerFeed' && <p>Supply: {record.spec.supply?.type ?? 'Unknown'} · Voltage: {record.spec.supply?.voltageV ?? 'Unknown'} V · Current rating: {record.spec.supply?.currentA ?? 'Unknown'} A · Usable budget and redundancy: Unknown</p>}
      {record.spec.upstreamSources?.map((source) => <p key={`${source.system}/${source.type}/${source.id}`}>Upstream: {source.system}/{source.type}/{source.id}</p>)}
      <ul>{inventory.slots.filter((slot) => slot.claim.metadata.namespace === record.metadata.namespace && (slot.claim.spec.placement.rackRef?.name === record.metadata.name && record.kind === 'Rack' || slot.claim.spec.placement.facilityRef?.name === record.metadata.name && record.kind === 'Facility')).map((slot) => <li key={slot.key}><a href={`inventory/${encodeURIComponent(slot.claim.metadata.namespace ?? '')}/${encodeURIComponent(slot.claim.metadata.name)}`}>{slot.claim.metadata.name}</a> — {slot.devices.length} registered assets; occupancy unverified</li>)}</ul>
    </details>)}
  </section>;
}
