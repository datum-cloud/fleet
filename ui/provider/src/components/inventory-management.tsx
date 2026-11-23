import { Input } from '@datum-cloud/datum-ui/input';
import { Label } from '@datum-cloud/datum-ui/label';
import { useContext, useId, useLayoutEffect, useRef, useState, type ComponentProps, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Button } from '@datum-cloud/datum-ui/button';
import { ApiError, PLUGIN_ID, PROVIDER_PROJECT } from '../lib/api';
import { canEditInventoryLabel, createInventoryRecord, editInventoryLabel, useInventoryPermission, type InventoryFormValues, type ManagedKind, type ManagedRecord } from '../lib/inventory-management';
import type { Device, DeviceClaim, PhysicalRecord } from '../../../shared/fleet-api';
import { InventoryOperationContext, InventoryOperationGenerationContext, InventoryOperationReadyContext, InventoryOperationInvalidated, InventoryOperationScope } from '../lib/inventory-operation';
import { InventoryHardwareFields } from './inventory-hardware-fields';
import type { Inventory } from '../inventory';
import { InventoryReadAccessContext, InventoryReferenceReadError, readFailureIssues, redactInventory } from '../lib/inventory-read-access';
import { INVENTORY_NAMESPACE, inInventoryNamespace } from '../lib/inventory-namespace';
import { useNavigate } from 'react-router';
import { confirmInventoryDismissal, InventoryDraftGuard, InventoryForm, InventoryFormSurface } from './inventory-form-surface';

export function InventoryManagement(props: ComponentProps<typeof InventoryManagementForm>) {
  return <InventoryManagementForm {...props} />;
}

function CreateTaskButton({ href, allowed, label }: { href: string; allowed: boolean; label: string }) {
  const navigate = useNavigate();
  return <Button type="secondary" theme="outline" size="small" htmlType="button" disabled={!allowed} onClick={() => { if (allowed) void navigate(href); }}>Create {label}</Button>;
}

function InventoryManagementForm({ kind, record, facilities: sourceFacilities, claims: sourceClaims = [], devices: sourceDevices = [], feeds: sourceFeeds = [], racks: sourceRacks = [], catalog: sourceCatalog = [], compact = false, issues: sourceIssues, createHref, task = false, onDone }: { kind: ManagedKind; record?: ManagedRecord; facilities: PhysicalRecord[]; claims?: DeviceClaim[]; devices?: Device[]; feeds?: PhysicalRecord[]; racks?: PhysicalRecord[]; catalog?: PhysicalRecord[]; compact?: boolean; issues?: Inventory['issues']; createHref?: string; task?: boolean; onDone?: () => void }) {
  const client = useQueryClient();
  const onReadFailure = useContext(InventoryReadAccessContext);
  const [readIssues, setReadIssues] = useState<Inventory['issues']>({});
  const issues = { ...sourceIssues, ...readIssues };
  const facilities = issues.Facility ? [] : sourceFacilities.filter(inInventoryNamespace);
  const claims = issues.DeviceClaim ? [] : sourceClaims.filter(inInventoryNamespace);
  const devices = issues.Device ? [] : sourceDevices.filter(inInventoryNamespace);
  const feeds = issues.PowerFeed ? [] : sourceFeeds.filter(inInventoryNamespace);
  const racks = issues.Rack ? [] : sourceRacks.filter(inInventoryNamespace);
  const catalog = issues.Manufacturer || issues.DeviceType ? [] : sourceCatalog.filter(inInventoryNamespace);
  const sessionScope = useContext(InventoryOperationContext);
  const ready = useContext(InventoryOperationReadyContext);
  const generation = useContext(InventoryOperationGenerationContext);
  const scope = useRef(new InventoryOperationScope());
  useLayoutEffect(() => {
    const current = new InventoryOperationScope();
    scope.current = current;
    const invalidate = () => { scope.current.invalidate(); setBaseline(undefined); setBlocked(true); };
    const visibility = () => { if (document.visibilityState === 'hidden') invalidate(); };
    window.addEventListener('pagehide', invalidate);
    document.addEventListener('visibilitychange', visibility);
    return () => {
      invalidate();
      started.current = false;
      window.removeEventListener('pagehide', invalidate);
      document.removeEventListener('visibilitychange', visibility);
    };
  }, []);
  const [baseline, setBaseline] = useState<ManagedRecord>();
  const namespace = INVENTORY_NAMESPACE;
  const editable = !record || canEditInventoryLabel(record);
  const permission = useInventoryPermission(kind, editable && !issues[kind] ? namespace : '', record ? 'patch' : 'create', record?.metadata.name);
  const dependenciesUnavailable = !record && ((kind === 'Device' && !!issues?.DeviceClaim) || (kind === 'Rack' && !!issues?.Facility) || (kind === 'DeviceClaim' && !!issues?.Facility && !!issues?.Rack) || (kind === 'Cable' && !!issues?.Device && !!issues?.PowerFeed));
  const visible = permission.data === true && !permission.error && editable && !dependenciesUnavailable && !issues[kind];
  const allowed = ready && visible && !permission.isFetching;
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [message, setMessage] = useState('');
  const [uncertain, setUncertain] = useState(false);
  const [initial, setInitial] = useState('');
  const started = useRef(false);
  const hintID = useId();
  const [values, setValues] = useState<InventoryFormValues>({ name: record?.metadata.name ?? '', namespace, displayName: record?.spec.displayName ?? '', facility: '', height: '' });
  useLayoutEffect(() => {
    scope.current.invalidate(); setBaseline(undefined); setBlocked(true);
  }, [ready, generation, permission.isFetching, visible]);
  const field = (name: keyof InventoryFormValues, value: string) => setValues(previous => ({ ...previous, [name]: value }));
  const kindLabel = kind === 'DeviceClaim' ? 'installation plan' : kind.toLowerCase();
  function begin() {
    if (!allowed) return;
    scope.current = new InventoryOperationScope();
    const next = { name: record?.metadata.name ?? '', namespace, displayName: record?.spec.displayName ?? '', facility: '', height: '' };
    if (record) setBaseline(structuredClone(record));
    setValues(next); setInitial(JSON.stringify(next));
    setOpen(true); setMessage(''); setBlocked(false); setUncertain(false);
  }
  useLayoutEffect(() => {
    if (task && allowed && !started.current) { started.current = true; begin(); }
  }, [task, allowed]);
  const dirty = open && JSON.stringify(values) !== initial;
  function dismiss() {
    if (task) { if (!pending) onDone?.(); return; }
    if (!confirmInventoryDismissal(dirty, pending, uncertain)) return;
    scope.current.invalidate(); setBaseline(undefined); setOpen(false);
    setValues({ name: '', namespace, displayName: '', facility: '', height: '' });
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    if (!allowed || pending || blocked) return;
    if (!record && values.catalog && (issues.DeviceType || issues.Manufacturer || !catalog.some(item => item.kind === 'DeviceType' && item.metadata.uid === values.catalog?.uid && item.metadata.namespace === namespace))) {
      setMessage('Selected catalog unavailable. Clear the selection or refresh before registration.');
      return;
    }
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
      if (error instanceof InventoryOperationInvalidated) { setMessage(error.message); setBlocked(true); setUncertain(error.dispatched); }
      else if (error instanceof InventoryReferenceReadError) {
        const denied = readFailureIssues(error);
        setReadIssues(previous => ({ ...previous, ...denied }));
        onReadFailure(error);
        setValues(previous => ({ ...previous,
          facility: denied?.Facility ? '' : previous.facility,
          claim: denied?.DeviceClaim ? undefined : previous.claim,
          planLocation: previous.planLocation && denied?.[previous.planLocation.kind] ? undefined : previous.planLocation,
          cableLanes: previous.cableLanes?.map(lane => ({ ...lane,
            a: lane.a && denied?.[lane.a.kind] ? undefined : lane.a,
            b: lane.b && denied?.[lane.b.kind] ? undefined : lane.b,
          })),
        }));
        if (record || error.status === 401) { setOpen(false); setBaseline(undefined); }
        setMessage(error.status === 401 ? 'Your session expired. Sign in through the staff portal before continuing.' : 'Related inventory is no longer readable. Restricted choices were removed; your other input is kept. Reopen inventory after viewing access is restored.');
        setBlocked(true);
        const queryKey = [PLUGIN_ID, 'inventory', PROVIDER_PROJECT];
        await client.cancelQueries({ queryKey });
        client.setQueriesData<Inventory>({ queryKey }, previous => previous ? redactInventory(previous, denied) : previous);
      }
      else if (error instanceof ApiError && error.status === 403) { setMessage('Your management permission changed. Your input is kept here. Ask your administrator for access before trying again.'); setBlocked(true); }
      else if (error instanceof ApiError && error.status === 401) { setMessage('Your session expired. Your input is kept here. Sign in through the staff portal before continuing.'); setBlocked(true); }
      else if (error instanceof ApiError && error.status === 409) { setMessage('The record already exists, changed, or has another writer. Your input is kept here. Refresh and review the record before continuing.'); setBlocked(true); }
      else if (error instanceof ApiError && error.status === 404) { setMessage('A referenced record is no longer available. Your input is kept here. Refresh inventory and review the references before continuing.'); setBlocked(true); }
      else if (error instanceof ApiError && error.status === 422) setMessage('The API could not accept these values. Review the fields and try again.');
      else { setMessage('The save outcome is unknown. Your input is kept here. Check inventory before attempting another save; do not assume the change failed.'); setBlocked(true); setUncertain(true); }
    } finally { setPending(false); }
  }
  // Not a status: the reason a record cannot be edited is the disabled button's tooltip and description.
  const hint = !editable ? { id: hintID, text: 'Display-name editing requires a portal-owned record. Other records need ownership review.' } : undefined;
  const status = <p role="status" className="inventory-management-status">{message || (!editable ? '' : !ready || permission.isFetching ? 'Checking management access…' : dependenciesUnavailable ? 'Related records are unavailable, not empty. Creation requires viewing access to its references.' : permission.error instanceof ApiError && permission.error.status === 401 ? 'Your session expired. Sign in again through the staff portal.' : open && blocked ? 'Close and reopen this form to continue.' : !visible ? 'Viewing and management access are separate. Ask your administrator for physical-inventory management access, or retry after a temporary failure.' : '')}</p>;
  if (createHref && !record) return <section className="inventory-management inventory-management-compact"><div className="inventory-management-controls"><CreateTaskButton href={createHref} allowed={allowed} label={kindLabel} /></div>{status}</section>;
  return <section className={`inventory-management ${compact ? 'inventory-management-compact' : ''}`}>
    {open && <InventoryDraftGuard dirty={dirty} pending={pending} uncertain={uncertain} />}
    <InventoryFormSurface open={open && permission.data === true && !permission.error && !issues[kind]} task={task} title={record ? 'Edit display name' : `Create ${kindLabel}`} status={status} hint={hint} onDismiss={dismiss} trigger={<Button type="secondary" theme="outline" size="small" htmlType="button" disabled={!allowed} aria-describedby={hint?.id} onClick={begin}>{record ? 'Edit display name' : `Create ${kindLabel}`}</Button>}>
    <InventoryForm onSubmit={save} message={message}>
      <fieldset className="inventory-form-gate" disabled={!allowed || blocked || pending}>
      <p>{record ? 'Only the display name changes. Identity, placement, geometry, connections, and history remain untouched.' : 'Create a physical record only. This does not verify equipment, reserve space, or provision hardware.'}</p>
      {!record && <Label>Record name<Input required pattern="[a-z0-9]([a-z0-9.-]*[a-z0-9])?" maxLength={253} value={values.name} onChange={event => field('name', event.target.value)} /></Label>}
      <Label>Display name<Input required maxLength={253} value={values.displayName} onChange={event => field('displayName', event.target.value)} /></Label>
      {!record && (kind === 'Device' || kind === 'Cable' || kind === 'DeviceClaim') && <InventoryHardwareFields kind={kind} values={values} onChange={setValues} namespace={namespace} claims={claims} devices={devices} feeds={feeds} locations={[...facilities, ...racks]} catalog={catalog} />}
    {!record && kind === 'Cable' && (issues?.Device || issues?.PowerFeed) && <p>Some endpoint records are unavailable. Only readable equipment and power feeds are offered; missing choices do not mean those records are absent.</p>}
    {!record && kind === 'DeviceClaim' && (issues?.Facility || issues?.Rack) && <p>Some location records are unavailable. Only readable facilities and racks are offered; missing choices do not mean those records are absent.</p>}
      {!record && kind === 'Rack' && <>
        <Label>Facility<select required value={values.facility} onChange={event => field('facility', event.target.value)}><option value="">Choose a facility</option>{facilities.filter(facility => facility.metadata.namespace === namespace && !facility.metadata.deletionTimestamp).map(facility => <option key={facility.metadata.name} value={facility.metadata.name}>{facility.spec.displayName} ({facility.metadata.name})</option>)}</select></Label>
        <Label>Rack height (U, optional)<Input type="number" min={1} max={100} step={1} value={values.height} onChange={event => field('height', event.target.value)} /></Label><p className="inventory-muted">Leave height blank if unknown. A rack record does not establish free space.</p>
      </>}
      </fieldset>
      <div className="inventory-form-actions"><Button type="secondary" theme="outline" size="small" htmlType="button" disabled={pending} onClick={dismiss}>{uncertain ? 'Leave form' : 'Cancel'}</Button><Button type="primary" theme="solid" size="small" htmlType="submit" disabled={!allowed || blocked || pending}>{pending ? 'Saving…' : record ? 'Save changes' : 'Create record'}</Button></div>
    </InventoryForm>
    </InventoryFormSurface>
    {task && !open && <Button type="secondary" theme="outline" htmlType="button" onClick={onDone}>Back to inventory</Button>}
  </section>;
}
