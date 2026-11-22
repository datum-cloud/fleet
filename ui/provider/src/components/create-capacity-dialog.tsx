import { Button } from '@datum-cloud/datum-ui/button';
import { Label } from '@datum-cloud/datum-ui/label';
import { useQuery } from '@tanstack/react-query';
import { useContext, useLayoutEffect, useRef, useState, type FormEvent } from 'react';
import type { Device } from '../../../shared/fleet-api';
import { capacityNameFor, ineligibleReason, registerCapacity, registrationDevices, registrationPermission, RegistrationUnknown } from '../lib/capacity-registration';
import { InventoryOperationContext, InventoryOperationReadyContext, InventoryOperationScope, type InventoryOperation } from '../lib/inventory-operation';
import { useInventoryRead } from '../lib/inventory-read';
import { confirmInventoryDismissal, InventoryDraftGuard, InventoryForm, InventoryFormSurface } from './inventory-form-surface';
import { InventorySessionBoundary } from './inventory-session-boundary';

export function CreateCapacityDialog({ onRegistered }: { onRegistered?: () => void }) {
  return <InventorySessionBoundary pending={<p>Checking registration session…</p>} unavailable={() => <p>Registration unavailable: a verified staff session is required.</p>}><CapacityRegistrationForm onRegistered={onRegistered} /></InventorySessionBoundary>;
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
      const result = await registerCapacity(selected, operationID.current, operation.current);
      setMessage(`Registered ${result.metadata.name} as Disabled/Retain. Cleaning is unconfigured; readiness and availability remain unknown.`);
      setSelected(undefined); setOpen(false); onRegistered?.();
    } catch (error) {
      setUnknown(error instanceof RegistrationUnknown);
      setMessage(error instanceof Error ? error.message : 'Registration unavailable.');
    } finally { setPending(false); }
  }
  const status = <>{message && <p role="alert">{message}</p>}{!available && <p>{permission.isPending ? 'Checking registration permission…' : permission.error ? 'Registration unavailable: authorization could not be checked.' : 'Registration denied or unavailable for this session. Synthetic previews cannot register capacity.'}</p>}</>;
  return <>
    <InventoryDraftGuard dirty={open && !!selected} pending={pending} uncertain={unknown} />
    <InventoryFormSurface open={open} title="Register inventory-backed capacity" status={status} onDismiss={dismiss} trigger={<Button type="primary" size="small" disabled={!available} onClick={() => {
      scope.startLifecycle(); operation.current = scope.begin(parent); operationID.current = crypto.randomUUID();
      setSelected(undefined); setUnknown(false); setMessage(''); setOpen(true);
    }}>Register capacity</Button>}>
      <InventoryForm onSubmit={event => void submit(event)} message={message}>
        <p>Choose a Device in default. This creates an inert Disabled/Retain record, not usable capacity. Cleaning remains explicitly unconfigured. No hardware is prepared or changed.</p>
        <fieldset disabled={pending || unknown || !available}>
          <Label>Device<select required value={selected?.metadata.uid ?? ''} onChange={event => {
            const device = devices.data?.find(item => item.metadata.uid === event.target.value);
            setSelected(device ? structuredClone(device) : undefined);
          }}><option value="">Choose an existing Device</option>{devices.data?.map(device => <option key={device.metadata.uid ?? device.metadata.name} value={device.metadata.uid ?? ''} disabled={!!ineligibleReason(device, devices.data)}>{device.metadata.name}{ineligibleReason(device, devices.data) ? ` — ${ineligibleReason(device, devices.data)}` : ''}</option>)}</select></Label>
          {devices.isPending && <p>Loading Devices…</p>}
          {devices.error && <p role="alert">Devices are unavailable or access is denied. Registration is blocked.</p>}
          {devices.data?.length === 0 && <p>No Devices are available in default.</p>}
          {selected && <p>Capacity name: <code>{capacityNameFor(selected)}</code>. It is derived from this Device, so the Device can back only one capacity. The Device reference {selected.metadata.name} is immutable. This selection retains its exact incarnation and version; it is never silently refreshed.</p>}
          <Button htmlType="submit" disabled={!selected || !!devices.error || pending || unknown}>Register Disabled capacity</Button>
        </fieldset>
        <Button htmlType="button" onClick={dismiss} disabled={pending}>Close</Button>
      </InventoryForm>
    </InventoryFormSurface>
  </>;
}
