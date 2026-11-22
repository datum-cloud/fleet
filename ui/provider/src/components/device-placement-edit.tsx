import { useContext, useLayoutEffect, useRef, useState, type ComponentProps, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { ApiError, PLUGIN_ID, PROVIDER_PROJECT } from '../lib/api';
import { canCorrectPlacement, correctDevicePlacement, placementFields, type PlacementFields } from '../lib/device-placement-edit';
import { useInventoryPermission } from '../lib/inventory-management';
import { InventoryOperationContext, InventoryOperationGenerationContext, InventoryOperationReadyContext, InventoryOperationInvalidated, InventoryOperationScope } from '../lib/inventory-operation';
import { InventoryReadAccessContext, InventoryReferenceReadError, readFailureIssues, redactInventory } from '../lib/inventory-read-access';

export function DevicePlacementEdit(props: ComponentProps<typeof DevicePlacementForm>) {
  const generation = useContext(InventoryOperationGenerationContext);
  const ready = useContext(InventoryOperationReadyContext);
  return ready ? <DevicePlacementForm key={generation} {...props} /> : null;
}

function DevicePlacementForm({ device, locations, issues }: { device: Device; locations: PhysicalRecord[]; issues?: Inventory['issues'] }) {
  const client = useQueryClient();
  const session = useContext(InventoryOperationContext);
  const readFailure = useContext(InventoryReadAccessContext);
  const scope = useRef(new InventoryOperationScope());
  const [baseline, setBaseline] = useState<Device>();
  const [fields, setFields] = useState<PlacementFields>(() => placementFields());
  const [pending, setPending] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [message, setMessage] = useState('');
  const [denied, setDenied] = useState<Inventory['issues']>({});
  const unavailable = { ...issues, ...denied };
  const namespace = device.metadata.namespace ?? '';
  const editable = canCorrectPlacement(device);
  const permission = useInventoryPermission('Device', editable ? namespace : '', 'patch', device.metadata.name);
  const allowed = editable && permission.data === true && !permission.isFetching && !permission.error && !unavailable.Device && !(device.spec.reportedPlacement?.facilityRef && unavailable.Facility) && !(device.spec.reportedPlacement?.rackRef && unavailable.Rack);
  const visible = locations.filter(record => !unavailable[record.kind] && record.metadata.namespace === namespace && !record.metadata.deletionTimestamp);
  const field = (key: keyof PlacementFields, value: string) => setFields(previous => ({ ...previous, [key]: value }));
  useLayoutEffect(() => {
    const invalidate = () => { scope.current.invalidate(); setBlocked(true); };
    const visibility = () => { if (document.visibilityState === 'hidden') invalidate(); };
    window.addEventListener('blur', invalidate);
    window.addEventListener('pagehide', invalidate);
    document.addEventListener('visibilitychange', visibility);
    return () => {
      scope.current.invalidate();
      window.removeEventListener('blur', invalidate);
      window.removeEventListener('pagehide', invalidate);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, []);
  useLayoutEffect(() => {
    scope.current.invalidate();
    setBlocked(true);
  }, [device.metadata.uid, device.metadata.resourceVersion, allowed, permission.isFetching, session]);
  useLayoutEffect(() => { setNeedsRefresh(false); }, [device.metadata.uid, device.metadata.resourceVersion]);
  useLayoutEffect(() => {
    if (!issues?.Facility && !issues?.Rack && !issues?.Device) return;
    scope.current.invalidate(); setBlocked(true); setBaseline(undefined);
    setFields(placementFields());
  }, [issues?.Facility, issues?.Rack, issues?.Device]);
  async function save(event: FormEvent) {
    event.preventDefault();
    if (!baseline || !allowed || pending || blocked) return;
    const operation = scope.current.begin(session);
    setPending(true); setMessage('');
    try {
      await correctDevicePlacement(baseline, fields, operation);
      operation.assertCurrent();
      setBaseline(undefined); setMessage('Reported location saved. No physical move or reservation was performed.');
      await client.invalidateQueries({ queryKey: [PLUGIN_ID, 'inventory', PROVIDER_PROJECT] });
    } catch (error) {
      setBlocked(true);
      setNeedsRefresh(true);
      if (error instanceof InventoryOperationInvalidated) setMessage(error.message);
      else if (error instanceof InventoryReferenceReadError) {
        const failures = readFailureIssues(error);
        setDenied(previous => ({ ...previous, ...failures }));
        setBaseline(undefined); setFields(placementFields()); readFailure(error);
        setMessage('Related inventory is unavailable. Restricted names and choices were removed. Reopen inventory after access is restored. No write was dispatched.');
        const queryKey = [PLUGIN_ID, 'inventory', PROVIDER_PROJECT];
        await client.cancelQueries({ queryKey });
        client.setQueriesData<Inventory>({ queryKey }, previous => previous ? redactInventory(previous, failures) : previous);
      } else if (error instanceof ApiError && error.status === 422) { setBlocked(false); setNeedsRefresh(false); setMessage('Review the location, geometry, observation time, reason, and evidence. The API did not accept these values.'); }
      else if (error instanceof ApiError && [401, 403].includes(error.status)) setMessage('Session or management access is no longer valid. Reopen inventory after access is restored.');
      else if (error instanceof ApiError && [404, 409].includes(error.status)) setMessage('The Device or a reference changed or disappeared. Refresh and reopen the form; do not retry this baseline.');
      else setMessage('The save outcome is unknown. Check the Device before another attempt; do not retry blindly.');
    } finally { setPending(false); }
  }
  return <section className="inventory-management" aria-label="Correct reported location">
    <h3>Reported location</h3>
    <p>Correct a record, not a physical move, installation handover, or reservation. Identity and claim binding stay unchanged. Blank geometry means unknown; model and claim dimensions are not copied.</p>
    {!editable ? <p>Legacy GitOps-managed or unavailable Devices are outside this editor.</p> : !allowed && <p>Device management permission must be confirmed before correcting location.</p>}
    {allowed && !baseline && !needsRefresh && <button type="button" onClick={() => {
      scope.current = new InventoryOperationScope();
      setBaseline(structuredClone(device));
      const next = placementFields(device.spec.reportedPlacement);
      if (unavailable.Facility) next.facility = '';
      if (unavailable.Rack) next.rack = '';
      setFields(next); setBlocked(false); setMessage('');
    }}>{device.spec.reportedPlacement ? 'Correct location' : 'Add reported location'}</button>}
    {baseline && <form className="inventory-form" onSubmit={save}>
      <p>Authenticated actor history uses existing resource audit logs.</p>
      <label>Actual facility (optional)<select value={fields.facility} disabled={!!unavailable.Facility} onChange={event => field('facility', event.target.value)}><option value="">Unknown / not recorded</option>{visible.filter(record => record.kind === 'Facility').map(record => <option key={record.metadata.name} value={record.metadata.name}>{record.spec.displayName || record.metadata.name}</option>)}</select></label>
      <label>Actual rack (optional)<select value={fields.rack} disabled={!!unavailable.Rack} onChange={event => field('rack', event.target.value)}><option value="">Unknown / not recorded</option>{visible.filter(record => record.kind === 'Rack' && (!fields.facility || record.spec.facilityRef?.name === fields.facility)).map(record => <option key={record.metadata.name} value={record.metadata.name}>{record.spec.displayName || record.metadata.name}</option>)}</select></label>
      <label>Reported position (U)<input type="number" min="1" max="100" step="any" value={fields.position} onChange={event => field('position', event.target.value)} /></label>
      <label>Reported height (U)<input type="number" min="0" max="100" step="any" value={fields.height} onChange={event => field('height', event.target.value)} /></label>
      <label>Reported face<select value={fields.face} onChange={event => field('face', event.target.value)}><option value="">Unknown</option><option value="front">Front</option><option value="rear">Rear</option></select></label>
      <label>Reported depth<select value={fields.fullDepth} onChange={event => field('fullDepth', event.target.value)}><option value="">Unknown</option><option value="true">Full depth</option><option value="false">Partial depth</option></select></label>
      <label>Non-unit mount (optional)<input value={fields.mount} maxLength={253} onChange={event => field('mount', event.target.value)} /></label>
      <label>Observation time (ISO 8601)<input required value={fields.observedAt} onChange={event => field('observedAt', event.target.value)} /></label>
      <label>Correction reason<input required maxLength={1024} value={fields.reason} onChange={event => field('reason', event.target.value)} /></label>
      <label>Evidence reference<input required maxLength={1024} value={fields.reference} onChange={event => field('reference', event.target.value)} /></label>
      <p>Do not include credentials or secrets in evidence.</p>
      <button type="submit" disabled={!allowed || pending || blocked}>{pending ? 'Saving location…' : 'Save reported location'}</button>
      <button type="button" disabled={pending} onClick={() => { scope.current.invalidate(); setBaseline(undefined); }}>Close location form</button>
      {blocked && <p>This form is no longer authorized to save. Refresh inventory and reopen it.</p>}
    </form>}
    {message && <p role="status">{message}</p>}
  </section>;
}
