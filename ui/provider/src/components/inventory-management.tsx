import { useState, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Button } from '@datum-cloud/datum-ui/button';
import { ApiError, PLUGIN_ID, PROVIDER_PROJECT } from '../lib/api';
import { canEditInventoryLabel, createInventoryRecord, editInventoryLabel, useInventoryPermission, type InventoryFormValues, type ManagedKind } from '../lib/inventory-management';
import type { PhysicalRecord } from '../../../shared/fleet-api';

export function InventoryManagement({ kind, record, facilities }: { kind: ManagedKind; record?: PhysicalRecord; facilities: PhysicalRecord[] }) {
  const client = useQueryClient();
  const [namespace, setNamespace] = useState(record?.metadata.namespace ?? 'default');
  const editable = !record || canEditInventoryLabel(record);
  const permission = useInventoryPermission(kind, editable ? namespace : '', record ? 'patch' : 'create', record?.metadata.name);
  const allowed = permission.data === true && !permission.isFetching && !permission.error && editable;
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [message, setMessage] = useState('');
  const [values, setValues] = useState<InventoryFormValues>({ name: record?.metadata.name ?? '', namespace, displayName: record?.spec.displayName ?? '', recordedBy: '', reference: '', facility: '', height: '' });
  const field = (name: keyof InventoryFormValues, value: string) => setValues(previous => ({ ...previous, [name]: value }));
  async function save(event: FormEvent) {
    event.preventDefault();
    if (!allowed || pending || blocked) return;
    setPending(true); setMessage('');
    try {
      if (record) await editInventoryLabel(record, values);
      else await createInventoryRecord(kind, { ...values, namespace });
      setMessage(record ? 'Display name saved.' : `${kind} record created.`); setOpen(false);
      await client.invalidateQueries({ queryKey: [PLUGIN_ID, 'inventory', PROVIDER_PROJECT] });
    } catch (error) {
      if (error instanceof ApiError && error.status === 403) { setMessage('Your management permission changed. Your input is kept here. Ask your administrator for access before trying again.'); setBlocked(true); }
      else if (error instanceof ApiError && error.status === 401) { setMessage('Your session expired. Your input is kept here. Sign in through the staff portal before continuing.'); setBlocked(true); }
      else if (error instanceof ApiError && error.status === 409) { setMessage('The record already exists, changed, or has another writer. Your input is kept here. Refresh and review the record before continuing.'); setBlocked(true); }
      else if (error instanceof ApiError && error.status === 422) setMessage('The API could not accept these values. Review the fields and try again.');
      else { setMessage('The save outcome is unknown. Your input is kept here. Check inventory before attempting another save; do not assume the change failed.'); setBlocked(true); }
    } finally { setPending(false); }
  }
  return <details className="inventory-management">
    <summary>Manage {record ? 'this record' : `${kind === 'Facility' ? 'facilities' : 'racks'}`}</summary>
    {!record && <label>Namespace<input aria-label="Management namespace" value={namespace} disabled={open || pending} onChange={event => setNamespace(event.target.value)} /></label>}
    {!editable ? <p>This record has another or unknown writer. It needs an explicit ownership review before portal editing. No ownership changes are made here.</p> : permission.isFetching ? <p role="status">Checking management access…</p> : permission.error instanceof ApiError && permission.error.status === 401 ? <p>Your session expired. Sign in again through the staff portal before continuing. Unsaved input remains here.</p> : !allowed && <p>Viewing and management access are separate. Ask your administrator about physical-inventory management access. A permission check must succeed before changes are available.</p>}
    {allowed && !open && <Button type="secondary" theme="outline" size="small" htmlType="button" onClick={() => { setOpen(true); setMessage(''); setBlocked(false); }}> {record ? 'Edit display name' : `Create ${kind.toLowerCase()}`} </Button>}
    {open && <form onSubmit={save} className="inventory-form">
      <p>{record ? 'Only the display name and recording details change. Identity, placement, geometry, connections, and history remain untouched.' : 'Create a physical record only. This does not verify equipment, reserve space, or provision hardware.'}</p>
      {!record && <label>Record name<input required maxLength={253} value={values.name} onChange={event => field('name', event.target.value)} /></label>}
      <label>Display name<input required maxLength={253} value={values.displayName} onChange={event => field('displayName', event.target.value)} /></label>
      {!record && kind === 'Rack' && <>
        <label>Facility<select required value={values.facility} onChange={event => field('facility', event.target.value)}><option value="">Choose a facility</option>{facilities.filter(facility => facility.metadata.namespace === namespace && !facility.metadata.deletionTimestamp).map(facility => <option key={facility.metadata.name} value={facility.metadata.name}>{facility.spec.displayName} ({facility.metadata.name})</option>)}</select></label>
        <label>Rack height (U, optional)<input type="number" min={1} max={100} step={1} value={values.height} onChange={event => field('height', event.target.value)} /></label><p className="inventory-muted">Leave height blank if unknown. A rack record does not establish free space.</p>
      </>}
      <label>Recorded by<input required maxLength={253} value={values.recordedBy} onChange={event => field('recordedBy', event.target.value)} /></label>
      <p className="inventory-muted">The recorder is an attributed observation, not a replacement for the API audit identity of your signed-in account.</p>
      <label>Change reference<input value={values.reference} maxLength={2048} onChange={event => field('reference', event.target.value)} /></label>
      <div className="flex flex-wrap gap-3"><Button type="primary" theme="solid" size="small" htmlType="submit" disabled={!allowed || blocked || pending}>{pending ? 'Saving…' : record ? 'Save display name' : `Create ${kind.toLowerCase()} record`}</Button><Button type="secondary" theme="outline" size="small" htmlType="button" disabled={pending} onClick={() => setOpen(false)}>Close form</Button></div>
    </form>}
    {message && <p role="status">{message}</p>}
  </details>;
}
