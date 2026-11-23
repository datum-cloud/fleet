import { Input } from '@datum-cloud/datum-ui/input';
import { Label } from '@datum-cloud/datum-ui/label';
import { Button } from '@datum-cloud/datum-ui/button';
import { useContext, useId, useLayoutEffect, useRef, useState, type ComponentProps, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import type { Inventory } from '../inventory';
import { ApiError, PLUGIN_ID, PROVIDER_PROJECT } from '../lib/api';
import { canCorrectPlacement, correctDevicePlacement, placementFields, type PlacementFields } from '../lib/device-placement-edit';
import { InventoryValidationError } from '../lib/inventory-hardware';
import { useInventoryPermission } from '../lib/inventory-management';
import { InventoryOperationContext, InventoryOperationGenerationContext, InventoryOperationReadyContext, InventoryOperationInvalidated, InventoryOperationScope } from '../lib/inventory-operation';
import { InventoryReadAccessContext, InventoryReferenceReadError, readFailureIssues, redactInventory } from '../lib/inventory-read-access';
import { confirmInventoryDismissal, InventoryDraftGuard, InventoryForm, InventoryFormSurface } from './inventory-form-surface';

export function DevicePlacementEdit(props: ComponentProps<typeof DevicePlacementForm>) {
  return <DevicePlacementForm {...props} />;
}

function DevicePlacementForm({ device, locations, issues }: { device: Device; locations: PhysicalRecord[]; issues?: Inventory['issues'] }) {
  const client = useQueryClient();
  const session = useContext(InventoryOperationContext);
  const ready = useContext(InventoryOperationReadyContext);
  const generation = useContext(InventoryOperationGenerationContext);
  const readFailure = useContext(InventoryReadAccessContext);
  const scope = useRef(new InventoryOperationScope());
  const [baseline, setBaseline] = useState<Device>();
  const [open, setOpen] = useState(false);
  const [fields, setFields] = useState<PlacementFields>(() => placementFields());
  const [pending, setPending] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [message, setMessage] = useState('');
  const [uncertain, setUncertain] = useState(false);
  const [initial, setInitial] = useState('');
  const [denied, setDenied] = useState<Inventory['issues']>({});
  const unavailable = { ...issues, ...denied };
  const namespace = device.metadata.namespace ?? '';
  const editable = canCorrectPlacement(device);
  const permission = useInventoryPermission('Device', editable ? namespace : '', 'patch', device.metadata.name);
  const permitted = editable && permission.data === true && !permission.error && !unavailable.Device && !(device.spec.reportedPlacement?.facilityRef && unavailable.Facility) && !(device.spec.reportedPlacement?.rackRef && unavailable.Rack);
  const allowed = ready && permitted && !permission.isFetching;
  const visible = locations.filter(record => !unavailable[record.kind] && record.metadata.namespace === namespace && !record.metadata.deletionTimestamp);
  const field = (key: keyof PlacementFields, value: string) => setFields(previous => ({ ...previous, [key]: value }));
  const hintID = useId();
  const evidenceHint = useId();
  // A rack implies its facility; a facility clears a rack that is not in it.
  const chooseFacility = (facility: string) => setFields(previous => ({ ...previous, facility, rack: !facility || visible.some(record => record.kind === 'Rack' && record.metadata.name === previous.rack && record.spec.facilityRef?.name === facility) ? previous.rack : '' }));
  const chooseRack = (rack: string) => setFields(previous => {
    const facility = visible.find(record => record.kind === 'Rack' && record.metadata.name === rack)?.spec.facilityRef?.name;
    return { ...previous, rack, facility: facility && visible.some(record => record.kind === 'Facility' && record.metadata.name === facility) ? facility : previous.facility };
  });
  useLayoutEffect(() => {
    const invalidate = () => { scope.current.invalidate(); setBlocked(true); setBaseline(undefined); };
    const visibility = () => { if (document.visibilityState === 'hidden') invalidate(); };
    window.addEventListener('pagehide', invalidate);
    document.addEventListener('visibilitychange', visibility);
    return () => {
      scope.current.invalidate();
      window.removeEventListener('pagehide', invalidate);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, []);
  useLayoutEffect(() => {
    scope.current.invalidate();
    setBlocked(true); setBaseline(undefined);
  }, [device.metadata.uid, device.metadata.resourceVersion, allowed, permission.isFetching, session, generation]);
  useLayoutEffect(() => { setNeedsRefresh(false); }, [device.metadata.uid, device.metadata.resourceVersion]);
  useLayoutEffect(() => {
    if (!issues?.Facility && !issues?.Rack && !issues?.Device) return;
    scope.current.invalidate(); setBlocked(true); setBaseline(undefined); setOpen(false);
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
      setBaseline(undefined); setOpen(false); setMessage('Location saved.');
      await client.invalidateQueries({ queryKey: [PLUGIN_ID, 'inventory', PROVIDER_PROJECT] });
    } catch (error) {
      setBlocked(true);
      setNeedsRefresh(true);
      if (error instanceof InventoryOperationInvalidated) { setMessage(error.message); setUncertain(error.dispatched); }
      else if (error instanceof InventoryReferenceReadError) {
        const failures = readFailureIssues(error);
        setDenied(previous => ({ ...previous, ...failures }));
        setBaseline(undefined); setOpen(false); setFields(placementFields()); readFailure(error);
        setMessage('Related inventory is unavailable. Restricted names and choices were removed. Reopen inventory after access is restored. No write was dispatched.');
        const queryKey = [PLUGIN_ID, 'inventory', PROVIDER_PROJECT];
        await client.cancelQueries({ queryKey });
        client.setQueriesData<Inventory>({ queryKey }, previous => previous ? redactInventory(previous, failures) : previous);
      } else if (error instanceof InventoryValidationError) { setBlocked(false); setNeedsRefresh(false); setMessage(error.message); }
      else if (error instanceof ApiError && error.status === 422) { setBlocked(false); setNeedsRefresh(false); setMessage('Values rejected — check the fields.'); }
      else if (error instanceof ApiError && [401, 403].includes(error.status)) setMessage('Session or management access is no longer valid. Reopen inventory after access is restored.');
      else if (error instanceof ApiError && [404, 409].includes(error.status)) setMessage('The Device or a reference changed or disappeared. Refresh and reopen the form; do not retry this baseline.');
      else { setMessage('The save outcome is unknown. Check the Device before another attempt; do not retry blindly.'); setUncertain(true); }
    } finally { setPending(false); }
  }
  const dirty = open && JSON.stringify(fields) !== initial;
  function dismiss() {
    if (!confirmInventoryDismissal(dirty, pending, uncertain)) return;
    scope.current.invalidate(); setBaseline(undefined); setOpen(false); setFields(placementFields());
  }
  // Why the trigger is unavailable is its tooltip and description; the status line carries save results only.
  const reason = !editable ? 'Managed elsewhere — not editable here' : !ready || permission.isFetching ? 'Checking access…' : !permitted ? 'Needs inventory management access' : needsRefresh ? 'Refresh to retry' : '';
  const hint = reason ? { id: hintID, text: reason } : undefined;
  const status = <p role="status" className="inventory-management-status">{message || (open && blocked ? 'Close and reopen to continue.' : '')}</p>;
  return <section className="inventory-management inventory-location-editor" aria-label="Correct reported location">
    {open && <InventoryDraftGuard dirty={dirty} pending={pending} uncertain={uncertain} />}
    <InventoryFormSurface open={open && !!permitted} title="Correct reported location" status={status} hint={hint} onDismiss={dismiss} trigger={<Button type="secondary" theme="outline" size="small" htmlType="button" disabled={!allowed || needsRefresh} aria-describedby={hint?.id} onClick={() => {
      if (!allowed) return;
      scope.current = new InventoryOperationScope();
      setOpen(true);
      setBaseline(structuredClone(device));
      const next = placementFields(device.spec.reportedPlacement);
      if (unavailable.Facility) next.facility = '';
      if (unavailable.Rack) next.rack = '';
      setFields(next); setInitial(JSON.stringify(next)); setBlocked(false); setMessage(''); setUncertain(false);
    }}>{device.spec.reportedPlacement ? 'Correct location' : 'Add reported location'}</Button>}>
    <InventoryForm onSubmit={save} message={message}>
      <fieldset className="inventory-form-gate" disabled={!allowed || blocked || pending}>
      <div className="inventory-form-grid">
        <Label>Facility<select value={fields.facility} disabled={!!unavailable.Facility} onChange={event => chooseFacility(event.target.value)}><option value="">Unknown / not recorded</option>{visible.filter(record => record.kind === 'Facility').map(record => <option key={record.metadata.name} value={record.metadata.name}>{record.spec.displayName || record.metadata.name}</option>)}</select></Label>
        <Label>Rack<select value={fields.rack} disabled={!!unavailable.Rack} onChange={event => chooseRack(event.target.value)}><option value="">Unknown / not recorded</option>{visible.filter(record => record.kind === 'Rack' && (!fields.facility || record.spec.facilityRef?.name === fields.facility)).map(record => <option key={record.metadata.name} value={record.metadata.name}>{record.spec.displayName || record.metadata.name}</option>)}</select></Label>
        <Label>Position (U)<Input type="number" min="1" max="100" step="any" value={fields.position} onChange={event => field('position', event.target.value)} /></Label>
        <Label>Height (U)<Input type="number" min="0" max="100" step="any" value={fields.height} onChange={event => field('height', event.target.value)} /></Label>
        <Label>Face<select value={fields.face} onChange={event => field('face', event.target.value)}><option value="">Unknown</option><option value="front">Front</option><option value="rear">Rear</option></select></Label>
        <Label>Depth<select value={fields.fullDepth} onChange={event => field('fullDepth', event.target.value)}><option value="">Unknown</option><option value="true">Full depth</option><option value="false">Partial depth</option></select></Label>
      </div>
      <Label>Mount (non-U)<Input value={fields.mount} maxLength={253} placeholder="shelf, 0U left" onChange={event => field('mount', event.target.value)} /></Label>
      <div className="inventory-form-grid inventory-form-rule">
        <Label>Observation time (ISO 8601)<Input required value={fields.observedAt} onChange={event => field('observedAt', event.target.value)} /></Label>
        <div className="inventory-field">
          <Label>Evidence reference<Input required maxLength={1024} aria-describedby={evidenceHint} value={fields.reference} onChange={event => field('reference', event.target.value)} /></Label>
          <small id={evidenceHint} className="inventory-field-hint">No credentials or secrets.</small>
        </div>
      </div>
      <Label>Correction reason<Input required maxLength={1024} value={fields.reason} onChange={event => field('reason', event.target.value)} /></Label>
      </fieldset>
      <div className="inventory-form-actions"><Button type="secondary" theme="outline" size="small" htmlType="button" disabled={pending} onClick={dismiss}>{uncertain ? 'Leave form' : 'Cancel'}</Button><Button type="primary" theme="solid" size="small" htmlType="submit" disabled={!allowed || pending || blocked}>{pending ? 'Saving location…' : 'Save changes'}</Button></div>
    </InventoryForm>
    </InventoryFormSurface>
  </section>;
}
