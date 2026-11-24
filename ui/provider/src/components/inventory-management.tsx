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
import { InventoryValidationError, recordNameFrom } from '../lib/inventory-hardware';
import type { Inventory } from '../inventory';
import { InventoryReadAccessContext, InventoryReferenceReadError, readFailureIssues, redactInventory } from '../lib/inventory-read-access';
import { INVENTORY_NAMESPACE, inInventoryNamespace } from '../lib/inventory-namespace';
import { useNavigate } from 'react-router';
import { FieldSelect } from './field-select';
import { confirmInventoryDismissal, InventoryDraftGuard, InventoryForm, InventoryFormSurface } from './inventory-form-surface';

export function InventoryManagement(props: ComponentProps<typeof InventoryManagementForm>) {
  return <InventoryManagementForm {...props} />;
}

function CreateTaskButton({ href, allowed, label, hint }: { href: string; allowed: boolean; label: string; hint?: { id: string; text: string } }) {
  const navigate = useNavigate();
  return <div className="inventory-management-controls" title={hint?.text}><Button type="secondary" theme="outline" size="small" htmlType="button" disabled={!allowed} aria-describedby={hint?.id} onClick={() => { if (allowed) void navigate(href); }}>Create {label}</Button>{hint && <span id={hint.id} className="inventory-visually-hidden">{hint.text}</span>}</div>;
}

function InventoryManagementForm({ kind, record, facilities: sourceFacilities, claims: sourceClaims = [], devices: sourceDevices = [], feeds: sourceFeeds = [], racks: sourceRacks = [], catalog: sourceCatalog = [], cables: sourceCables = [], compact = false, issues: sourceIssues, createHref, task = false, onDone }: { kind: ManagedKind; record?: ManagedRecord; facilities: PhysicalRecord[]; claims?: DeviceClaim[]; devices?: Device[]; feeds?: PhysicalRecord[]; racks?: PhysicalRecord[]; catalog?: PhysicalRecord[]; cables?: PhysicalRecord[]; compact?: boolean; issues?: Inventory['issues']; createHref?: string; task?: boolean; onDone?: () => void }) {
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
  const cables = issues.Cable ? [] : sourceCables.filter(inInventoryNamespace);
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
  const permission = useInventoryPermission(kind, editable && !issues[kind] ? namespace : '', record ? 'patch' : 'create');
  const dependenciesUnavailable = !record && ((kind === 'Device' && !!issues?.DeviceClaim) || (kind === 'Rack' && !!issues?.Facility) || (kind === 'DeviceClaim' && !!issues?.Facility && !!issues?.Rack) || (kind === 'Cable' && !!issues?.Device && !!issues?.PowerFeed));
  const visible = permission.data === true && !permission.error && editable && !dependenciesUnavailable && !issues[kind];
  const allowed = ready && visible && !permission.isFetching;
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [message, setMessage] = useState('');
  const [uncertain, setUncertain] = useState(false);
  const [initial, setInitial] = useState('');
  // The record name follows the display name until it is edited.
  const [nameEdited, setNameEdited] = useState(false);
  const started = useRef(false);
  const hintID = useId();
  const [values, setValues] = useState<InventoryFormValues>({ name: record?.metadata.name ?? '', namespace, displayName: record?.spec.displayName ?? '', facility: '', height: '' });
  useLayoutEffect(() => {
    scope.current.invalidate(); setBaseline(undefined); setBlocked(true);
  }, [ready, generation, permission.isFetching, visible]);
  const field = (name: keyof InventoryFormValues, value: string) => setValues(previous => ({ ...previous, [name]: value }));
  const kindLabel = kind === 'DeviceClaim' ? 'installation plan' : kind.toLowerCase();
  const displayName = (value: string) => setValues(previous => ({ ...previous, displayName: value, ...(!record && !nameEdited && { name: recordNameFrom(value) }) }));
  function begin() {
    if (!allowed) return;
    scope.current = new InventoryOperationScope();
    const next = { name: record?.metadata.name ?? '', namespace, displayName: record?.spec.displayName ?? '', facility: '', height: '' };
    if (record) setBaseline(structuredClone(record));
    setValues(next); setInitial(JSON.stringify(next));
    setOpen(true); setMessage(''); setBlocked(false); setUncertain(false); setNameEdited(false);
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
      setMessage(record ? 'Display name saved.' : `${kindLabel[0].toUpperCase()}${kindLabel.slice(1)} created.`); setOpen(false);
      await client.invalidateQueries({ queryKey: [PLUGIN_ID, 'inventory', PROVIDER_PROJECT] });
    } catch (error) {
      // A rule the form checked itself: no request was sent, so its own message is shown, not the API's.
      if (error instanceof InventoryValidationError) { setMessage(error.message); if (error.status === 409) setBlocked(true); }
      else if (error instanceof InventoryOperationInvalidated) { setMessage(error.message); setBlocked(true); setUncertain(error.dispatched); }
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
            aSource: lane.aSource && denied?.[lane.aSource.split('/')[0] as 'Device' | 'PowerFeed'] ? undefined : lane.aSource,
            bSource: lane.bSource && denied?.[lane.bSource.split('/')[0] as 'Device' | 'PowerFeed'] ? undefined : lane.bSource,
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
  // Not a status: why the trigger is unavailable is its tooltip and description. The status line carries save results only.
  const reason = !editable ? 'Managed elsewhere — not editable here' : !ready || permission.isFetching ? 'Checking access…' : dependenciesUnavailable ? 'References unreadable' : permission.error instanceof ApiError && permission.error.status === 401 ? 'Session expired — sign in again' : !visible ? 'Needs inventory management access' : '';
  const hint = reason ? { id: hintID, text: reason } : undefined;
  // A task page has no trigger to carry the tooltip, so there the reason is the page's only line.
  const status = <p role="status" className="inventory-management-status">{message || (open && blocked ? 'Close and reopen to continue.' : task && !open ? reason : '')}</p>;
  if (createHref && !record) return <section className="inventory-management inventory-management-compact"><CreateTaskButton href={createHref} allowed={allowed} label={kindLabel} hint={hint} />{status}</section>;
  // Display name first; the record name follows it until edited.
  const names = <div className="inventory-form-grid">
    <Label>Display name<Input required maxLength={253} value={values.displayName} onChange={event => displayName(event.target.value)} /></Label>
    {!record && <Label>Record name<Input required pattern="[a-z0-9]([a-z0-9.\-]*[a-z0-9])?" maxLength={253} placeholder="lowercase, digits, - and ." value={values.name} onChange={event => { setNameEdited(true); field('name', event.target.value); }} /></Label>}
  </div>;
  return <section className={`inventory-management ${compact ? 'inventory-management-compact' : ''}`}>
    {open && <InventoryDraftGuard dirty={dirty} pending={pending} uncertain={uncertain} />}
    <InventoryFormSurface open={open && permission.data === true && !permission.error && !issues[kind]} task={task} title={record ? 'Edit display name' : `Create ${kindLabel}`} status={status} hint={hint} onDismiss={dismiss} trigger={<Button type="secondary" theme="outline" size="small" htmlType="button" disabled={!allowed} aria-describedby={hint?.id} onClick={begin}>{record ? 'Edit display name' : `Create ${kindLabel}`}</Button>}>
    <InventoryForm onSubmit={save} message={message}>
      <fieldset className="inventory-form-gate" disabled={!allowed || blocked || pending}>
      {/* Unreadable collections leave a list incomplete, never empty: the gap is stated in amber. */}
      {!record && kind === 'Cable' && (issues?.Device || issues?.PowerFeed) && <p className="inventory-attention-note">Some devices or feeds unreadable — list incomplete</p>}
      {!record && kind === 'DeviceClaim' && (issues?.Facility || issues?.Rack) && <p className="inventory-attention-note">Some facilities or racks unreadable — list incomplete</p>}
      {kind !== 'Device' || record ? names : null}
      {!record && (kind === 'Device' || kind === 'Cable' || kind === 'DeviceClaim') && <InventoryHardwareFields kind={kind} values={values} onChange={setValues} namespace={namespace} claims={claims} devices={devices} feeds={feeds} cables={cables} locations={[...facilities, ...racks]} catalog={catalog} names={names} />}
      {!record && kind === 'Rack' && <>
        <Label>Facility<FieldSelect required value={values.facility} onValueChange={value => field('facility', value)} placeholder="Choose a facility" options={facilities.filter(facility => facility.metadata.namespace === namespace && !facility.metadata.deletionTimestamp).map(facility => ({ value: facility.metadata.name, label: `${facility.spec.displayName} (${facility.metadata.name})` }))} /></Label>
        <Label>Height (U)<Input type="number" min={1} max={100} step={1} placeholder="Leave blank if unknown" value={values.height} onChange={event => field('height', event.target.value)} /></Label>
      </>}
      </fieldset>
      <div className="inventory-form-actions"><Button type="secondary" theme="outline" size="small" htmlType="button" disabled={pending} onClick={dismiss}>{uncertain ? 'Leave form' : 'Cancel'}</Button><Button type="primary" theme="solid" size="small" htmlType="submit" disabled={!allowed || blocked || pending}>{pending ? 'Saving…' : record ? 'Save changes' : 'Create record'}</Button></div>
    </InventoryForm>
    </InventoryFormSurface>
    {task && !open && <Button type="secondary" theme="outline" htmlType="button" onClick={onDone}>Back to inventory</Button>}
  </section>;
}
