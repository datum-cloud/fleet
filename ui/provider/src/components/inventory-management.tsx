import { useContext, useLayoutEffect, useRef, useState, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Button } from '@datum-cloud/datum-ui/button';
import { ApiError, PLUGIN_ID, PROVIDER_PROJECT } from '../lib/api';
import { canEditInventoryLabel, createInventoryRecord, editInventoryLabel, useInventoryPermission, type InventoryFormValues, type ManagedKind, type ManagedRecord } from '../lib/inventory-management';
import type { Device, DeviceClaim, PhysicalRecord } from '../../../shared/fleet-api';
import { InventoryOperationContext, InventoryOperationInvalidated, InventoryOperationScope } from '../lib/inventory-operation';
import { InventoryHardwareFields } from './inventory-hardware-fields';
import type { Inventory } from '../inventory';

export function InventoryManagement({ kind, record, facilities, claims = [], devices = [], feeds = [], racks = [], compact = false, issues }: { kind: ManagedKind; record?: ManagedRecord; facilities: PhysicalRecord[]; claims?: DeviceClaim[]; devices?: Device[]; feeds?: PhysicalRecord[]; racks?: PhysicalRecord[]; compact?: boolean; issues?: Inventory['issues'] }) {
  const client = useQueryClient();
  const sessionScope = useContext(InventoryOperationContext);
  const scope = useRef(new InventoryOperationScope());
  useLayoutEffect(() => {
    const current = new InventoryOperationScope();
    scope.current = current;
    const invalidate = () => current.invalidate();
    const visibility = () => { if (document.visibilityState === 'hidden') invalidate(); };
    window.addEventListener('blur', invalidate);
    window.addEventListener('pagehide', invalidate);
    document.addEventListener('visibilitychange', visibility);
    return () => {
      invalidate();
      window.removeEventListener('blur', invalidate);
      window.removeEventListener('pagehide', invalidate);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, []);
  const [baseline, setBaseline] = useState<ManagedRecord>();
  const [namespace, setNamespace] = useState(record?.metadata.namespace ?? 'default');
  const editable = !record || canEditInventoryLabel(record);
  const permission = useInventoryPermission(kind, editable ? namespace : '', record ? 'patch' : 'create', record?.metadata.name);
  const dependenciesUnavailable = !record && ((kind === 'Device' && !!issues?.DeviceClaim) || (kind === 'Rack' && !!issues?.Facility) || (kind === 'DeviceClaim' && !!issues?.Facility && !!issues?.Rack) || (kind === 'Cable' && !!issues?.Device && !!issues?.PowerFeed));
  const allowed = permission.data === true && !permission.isFetching && !permission.error && editable && !dependenciesUnavailable;
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [message, setMessage] = useState('');
  const [values, setValues] = useState<InventoryFormValues>({ name: record?.metadata.name ?? '', namespace, displayName: record?.spec.displayName ?? '', recordedBy: '', reference: '', facility: '', height: '' });
  const field = (name: keyof InventoryFormValues, value: string) => setValues(previous => ({ ...previous, [name]: value }));
  const kindLabel = kind === 'DeviceClaim' ? 'installation plan' : kind.toLowerCase();
  async function save(event: FormEvent) {
    event.preventDefault();
    if (!allowed || pending || blocked) return;
    setPending(true); setMessage('');
    const operation = scope.current.begin(sessionScope);
    try {
      if (record) {
        if (!baseline) throw new ApiError(409, 'Reopen the form to review this record.');
        await editInventoryLabel(baseline, values, operation);
      } else await createInventoryRecord(kind, { ...values, namespace }, operation);
      operation.assertCurrent();
      setMessage(record ? 'Display name saved.' : `${kind} record created.`); setOpen(false);
      await client.invalidateQueries({ queryKey: [PLUGIN_ID, 'inventory', PROVIDER_PROJECT] });
    } catch (error) {
      if (error instanceof InventoryOperationInvalidated) { setMessage(error.message); setBlocked(true); }
      else if (error instanceof ApiError && error.status === 403) { setMessage('Your management permission changed. Your input is kept here. Ask your administrator for access before trying again.'); setBlocked(true); }
      else if (error instanceof ApiError && error.status === 401) { setMessage('Your session expired. Your input is kept here. Sign in through the staff portal before continuing.'); setBlocked(true); }
      else if (error instanceof ApiError && error.status === 409) { setMessage('The record already exists, changed, or has another writer. Your input is kept here. Refresh and review the record before continuing.'); setBlocked(true); }
      else if (error instanceof ApiError && error.status === 404) { setMessage('A referenced record is no longer available. Your input is kept here. Refresh inventory and review the references before continuing.'); setBlocked(true); }
      else if (error instanceof ApiError && error.status === 422) setMessage('The API could not accept these values. Review the fields and try again.');
      else { setMessage('The save outcome is unknown. Your input is kept here. Check inventory before attempting another save; do not assume the change failed.'); setBlocked(true); }
    } finally { setPending(false); }
  }
  const Container = compact ? 'section' : 'details';
  const Access = compact ? 'details' : 'div';
  return <Container className={`inventory-management ${compact ? 'inventory-management-compact' : ''}`}>
    {!compact && <summary>Manage {record ? 'this record' : `${kind === 'Facility' ? 'facilities' : `${kindLabel}s`}`}</summary>}
    <div className="inventory-management-controls"><Access className="inventory-management-access">
    {compact && <summary>{record ? 'Editing access' : 'Namespace & access'}</summary>}
    {!record && <label>Namespace<input aria-label="Management namespace" value={namespace} disabled={open || pending} onChange={event => setNamespace(event.target.value)} /></label>}
    {dependenciesUnavailable && <p>Related records are unavailable, not empty. Creation needs readable location, installation, or endpoint records. Ask your administrator about viewing access, or try again after a temporary failure.</p>}
    {!editable ? <p>This record has another or unknown writer. It needs an explicit ownership review before portal editing. Import provenance is history, not Git ownership; this portal has not established a writable handover for this record. No ownership changes are made here.</p> : permission.isFetching ? <p role="status">Checking management access…</p> : permission.error instanceof ApiError && permission.error.status === 401 ? <p>Your session expired. Sign in again through the staff portal before continuing. Unsaved input remains here.</p> : !allowed && <p>Viewing and management access are separate. Ask your administrator about physical-inventory management access. A permission check must succeed before changes are available.</p>}
    </Access>
    {allowed && !open && <Button type="secondary" theme="outline" size="small" htmlType="button" onClick={() => {
      if (record) {
        const snapshot = structuredClone(record);
        setBaseline(snapshot);
        setValues(previous => ({ ...previous, name: snapshot.metadata.name, displayName: snapshot.spec.displayName ?? '', namespace: snapshot.metadata.namespace ?? '' }));
      }
      setOpen(true); setMessage(''); setBlocked(false);
    }}> {record ? 'Edit display name' : `Create ${kindLabel}`} </Button>}
    </div>
    {open && <form onSubmit={save} className="inventory-form">
      <p>{record ? 'Only the display name and recording details change. Identity, placement, geometry, connections, and history remain untouched.' : 'Create a physical record only. This does not verify equipment, reserve space, or provision hardware.'}</p>
      {!record && <label>Record name<input required maxLength={253} value={values.name} onChange={event => field('name', event.target.value)} /></label>}
      <label>Display name<input required maxLength={253} value={values.displayName} onChange={event => field('displayName', event.target.value)} /></label>
      {!record && (kind === 'Device' || kind === 'Cable' || kind === 'DeviceClaim') && <InventoryHardwareFields kind={kind} values={values} onChange={setValues} namespace={namespace} claims={claims} devices={devices} feeds={feeds} locations={[...facilities, ...racks]} />}
    {!record && kind === 'Cable' && (issues?.Device || issues?.PowerFeed) && <p>Some endpoint records are unavailable. Only readable equipment and power feeds are offered; missing choices do not mean those records are absent.</p>}
    {!record && kind === 'DeviceClaim' && (issues?.Facility || issues?.Rack) && <p>Some location records are unavailable. Only readable facilities and racks are offered; missing choices do not mean those records are absent.</p>}
      {!record && kind === 'Rack' && <>
        <label>Facility<select required value={values.facility} onChange={event => field('facility', event.target.value)}><option value="">Choose a facility</option>{facilities.filter(facility => facility.metadata.namespace === namespace && !facility.metadata.deletionTimestamp).map(facility => <option key={facility.metadata.name} value={facility.metadata.name}>{facility.spec.displayName} ({facility.metadata.name})</option>)}</select></label>
        <label>Rack height (U, optional)<input type="number" min={1} max={100} step={1} value={values.height} onChange={event => field('height', event.target.value)} /></label><p className="inventory-muted">Leave height blank if unknown. A rack record does not establish free space.</p>
      </>}
      <label>Recorded by<input required maxLength={253} value={values.recordedBy} onChange={event => field('recordedBy', event.target.value)} /></label>
      <p className="inventory-muted">The recorder is an attributed observation, not a replacement for the API audit identity of your signed-in account.</p>
      <label>Change reference<input value={values.reference} maxLength={1024} onChange={event => field('reference', event.target.value)} /></label>
      <div className="flex flex-wrap gap-3"><Button type="primary" theme="solid" size="small" htmlType="submit" disabled={!allowed || blocked || pending}>{pending ? 'Saving…' : record ? 'Save display name' : `Create ${kindLabel} record`}</Button><Button type="secondary" theme="outline" size="small" htmlType="button" disabled={pending} onClick={() => setOpen(false)}>Close form</Button></div>
    </form>}
    {message && <p role="status">{message}</p>}
  </Container>;
}
