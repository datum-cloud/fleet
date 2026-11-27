import { Spinner } from '@datum-cloud/datum-ui/spinner';
import { Button } from '@datum-cloud/datum-ui/button';
import { Label } from '@datum-cloud/datum-ui/label';
import { useQuery } from '@tanstack/react-query';
import { useContext, useId, useLayoutEffect, useRef, useState, type FormEvent } from 'react';
import type { Device } from '../../../shared/fleet-api';
import { capacityNameFor, ineligibleReason, registerCapacity, registrationDevices, registrationPermission, RegistrationUnknown } from '../lib/capacity-registration';
import { InventoryOperationContext, InventoryOperationReadyContext, InventoryOperationScope, type InventoryOperation } from '../lib/inventory-operation';
import { useInventoryRead } from '../lib/inventory-read';
import { FieldSelect } from './field-select';
import { confirmInventoryDismissal, InventoryDraftGuard, InventoryForm, InventoryFormSurface } from './inventory-form-surface';
import { InventorySessionBoundary } from './inventory-session-boundary';

export function CreateCapacityDialog({ onRegistered }: { onRegistered?: () => void }) {
  return <InventorySessionBoundary pending={<UnavailableTrigger reason="Checking registration session…" />} unavailable={() => <UnavailableTrigger reason="Registration unavailable: a verified staff session is required." />}><CapacityRegistrationForm onRegistered={onRegistered} /></InventorySessionBoundary>;
}

// Without a verified session the trigger is disabled and opens nothing; the
// reason is its tooltip and accessible description.
function UnavailableTrigger({ reason }: { reason: string }) {
  const id = useId();
  return <div className="inventory-management-controls" title={reason}><Button type="primary" size="small" disabled aria-describedby={id}>Register capacity</Button><span id={id} className="inventory-visually-hidden">{reason}</span></div>;
}

export function CapacityRegistrationForm({ onRegistered }: { onRegistered?: () => void }) {
  const parent = useContext(InventoryOperationContext);
  const ready = useContext(InventoryOperationReadyContext);
  const [scope] = useState(() => new InventoryOperationScope());
  const operation = useRef<InventoryOperation | undefined>(undefined);
  const operationID = useRef('');
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<Device>();
  const [pending, setPending] = useState(false);
  const [unknown, setUnknown] = useState(false);
  const [message, setMessage] = useState('');
  const hintID = useId();
  const readPermission = useInventoryRead(registrationPermission);
  const permission = useQuery({ queryKey: ['capacity-registration-permission'], queryFn: readPermission, retry: false, refetchOnWindowFocus: false });
  const readDevices = useInventoryRead(registrationDevices);
  const devices = useQuery({ queryKey: ['capacity-registration-devices'], queryFn: readDevices, enabled: open && permission.data === true, retry: false, refetchOnWindowFocus: false });
  useLayoutEffect(() => { scope.startLifecycle(); return () => scope.invalidate(); }, [scope]);
  const available = ready && !!parent && permission.data === true;
  const dismiss = () => {
    if (confirmInventoryDismissal(!!selected, pending, unknown)) { scope.invalidate(); setOpen(false); }
  };
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!available || pending || unknown || !selected || !operation.current) return;
    setPending(true); setMessage('');
    try {
      // The new row in the table is the confirmation; no message outlives the dialog.
      await registerCapacity(selected, operationID.current, operation.current);
      setSelected(undefined); setOpen(false); onRegistered?.();
    } catch (error) {
      setUnknown(error instanceof RegistrationUnknown);
      setMessage(error instanceof Error ? error.message : 'Registration unavailable.');
    } finally { setPending(false); }
  }
  const status = message ? <p role="alert">{message}</p> : null;
  const hint = available ? undefined : { id: hintID, text: permission.isPending ? 'Checking registration permission…' : permission.error ? 'Registration unavailable: authorization could not be checked.' : 'Registration denied or unavailable for this session.' };
  return <>
    <InventoryDraftGuard dirty={open && !!selected} pending={pending} uncertain={unknown} />
    <InventoryFormSurface open={open} title="Register inventory-backed capacity" description="Creates a Disabled/Retain MachineCapacity from an Active Device in default. No hardware is prepared or changed." status={status} hint={hint} onDismiss={dismiss} trigger={<Button type="primary" size="small" disabled={!available} aria-describedby={hint?.id} onClick={() => {
      scope.startLifecycle(); operation.current = scope.begin(parent); operationID.current = crypto.randomUUID();
      setSelected(undefined); setUnknown(false); setMessage(''); setOpen(true);
    }}>Register capacity</Button>}>
      <InventoryForm onSubmit={event => void submit(event)} message={message}>
        <fieldset disabled={pending || unknown || !available}>
          <Label>Device<FieldSelect required value={selected?.metadata.uid ?? ''} onValueChange={value => {
            const device = devices.data?.find(item => item.metadata.uid === value);
            setSelected(device ? structuredClone(device) : undefined);
          }} placeholder="Choose an existing Device" options={devices.data?.map(device => {
            const reason = ineligibleReason(device, devices.data);
            // A Device without a uid is listed as ineligible; its placeholder value never matches a selection.
            return { value: device.metadata.uid ?? `unidentified/${device.metadata.name}`, disabled: !!reason, label: `${device.metadata.name}${reason ? ` — ${reason}` : ''}` };
          })} /></Label>
          {devices.isPending && <p role="status" className="inventory-loading"><Spinner aria-hidden="true" />Loading Devices…</p>}
          {devices.error && <p role="alert">Devices are unavailable or access is denied. Registration is blocked.</p>}
          {devices.data?.length === 0 && <p>No Devices are available in default.</p>}
          {selected && <p title={`Derived from the Device, so it can back only one capacity. The reference to ${selected.metadata.name} and its exact version are kept; the selection is never refreshed.`}>Capacity name: <code>{capacityNameFor(selected)}</code></p>}
          <Button htmlType="submit" disabled={!selected || !!devices.error || pending || unknown}>Register Disabled capacity</Button>
        </fieldset>
        <Button htmlType="button" onClick={dismiss} disabled={pending}>Close</Button>
      </InventoryForm>
    </InventoryFormSurface>
  </>;
}
