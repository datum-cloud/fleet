// Staff power on the Device page: the observed power state and the latest
// operation in the header strip, the Power control in the header actions, and
// the Operations tab. See "Device page: power" in
// docs/development/staff-inventory-ui.md.
import { Button } from '@datum-cloud/datum-ui/button';
import { Input } from '@datum-cloud/datum-ui/input';
import { Label } from '@datum-cloud/datum-ui/label';
import { useQueryClient } from '@tanstack/react-query';
import { useContext, useEffect, useId, useLayoutEffect, useRef, useState, type FormEvent } from 'react';
import type { Device, DeviceCustody, DevicePowerOperation, PowerAction } from '../../../shared/fleet-api';
import { ApiError } from '../lib/api';
import { comparedSystem, powerWord, staleReason } from '../lib/device-discovery';
import {
  ACTION_WORDS, createPowerOperation, destructive, evidenceNote, offeredActions, powerBlock, powerChange, powerOutcome, POWER_QUERY_KEY,
  useCaller, useCustody, usePowerOperations, usePowerPermission, type PowerOutcome,
} from '../lib/device-power';
import { displayLabel } from '../lib/display-names';
import { InventoryOperationContext, InventoryOperationGenerationContext, InventoryOperationInvalidated, InventoryOperationReadyContext, InventoryOperationScope } from '../lib/inventory-operation';
import { formatAge } from '../lib/telemetry';
import { FieldSelect } from './field-select';
import { InventoryDraftGuard, InventoryForm, InventoryFormSurface } from './inventory-form-surface';
import { SmallTable } from './inventory-record-layout';

const HOUR = 3_600_000;

/** A phase badge: the reason and any recovery hold are its tooltip and accessible text. */
export function PowerOutcomeBadge({ outcome, prefix }: { outcome: PowerOutcome; prefix?: string }) {
  const note = [outcome.reason, outcome.hold].filter(Boolean).join('\n') || undefined;
  return <span className="inventory-discovery-chip inventory-power-outcome" data-tone={outcome.tone} title={note}>
    {prefix && <>{prefix} · </>}{outcome.label}{outcome.hold && <span aria-hidden="true"> · held</span>}
    {note && <span className="inventory-visually-hidden">. {note}</span>}
  </span>;
}

/**
 * The header's power fact: the observed state, then the latest operation
 * while it runs, for an hour after it ends, or while custody holds it.
 */
export function DevicePowerFact({ device }: { device: Device }) {
  const operations = usePowerOperations(device);
  const custody = useCustody(device);
  const system = comparedSystem(device);
  const stale = staleReason(device.status?.discovery?.inventory, device);
  const state = powerWord(system?.powerState);
  const latest = operations.data?.[0];
  const outcome = latest && powerOutcome(latest, custody.data);
  const finished = latest?.status?.finishedAt ? Date.parse(latest.status.finishedAt) : NaN;
  const recent = outcome && (outcome.tone === 'progress' || !!outcome.hold || !(Date.now() - finished > HOUR));
  if (!state && !recent) return null;
  return <div className="inventory-power-fact"><dt>Power</dt><dd>
    {state && <span className={stale ? 'inventory-discovery-mark' : undefined} data-tone={stale ? 'warning' : undefined} title={stale ?? `Observed ${device.status?.discovery?.inventory?.observedAt ?? ''}`}>{state}{stale && <span className="inventory-visually-hidden">. Check: {stale}</span>}</span>}
    {recent && latest && <> <PowerOutcomeBadge outcome={outcome} prefix={ACTION_WORDS[latest.spec.action] ?? latest.spec.action} /></>}
  </dd></div>;
}

function failureMessage(error: unknown, sent: boolean): { message: string; uncertain: boolean } {
  if (error instanceof InventoryOperationInvalidated) return { message: error.dispatched ? 'Outcome unknown: the session changed after sending. Check Operations before another request.' : 'The session changed. Nothing was sent.', uncertain: error.dispatched };
  if (error instanceof ApiError && error.status === 422) return { message: `Rejected: ${error.message}`, uncertain: false };
  if (error instanceof ApiError && [401, 403].includes(error.status)) return { message: 'Power operator access is no longer valid. Nothing was created.', uncertain: false };
  // Before the POST went out, nothing can have been created.
  if (!sent) return { message: 'Nothing was sent. Try again.', uncertain: false };
  return { message: 'Outcome unknown. Check Operations before another request.', uncertain: true };
}

/**
 * The Power button and its dialog. Hidden without the power operator role;
 * disabled, with the reason as its tooltip, when the Device cannot be powered
 * now. A destructive action asks again, naming the server.
 */
export function DevicePowerControl({ device }: { device: Device }) {
  const client = useQueryClient();
  const session = useContext(InventoryOperationContext);
  const ready = useContext(InventoryOperationReadyContext);
  const generation = useContext(InventoryOperationGenerationContext);
  const permission = usePowerPermission(true);
  const permitted = permission.data === true && !permission.error;
  const caller = useCaller(permitted);
  const custody = useCustody(device);
  const operations = usePowerOperations(device);
  const scope = useRef(new InventoryOperationScope());
  const [open, setOpen] = useState(false);
  const [action, setAction] = useState('');
  const [reason, setReason] = useState('');
  const [confirming, setConfirming] = useState(false);
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState('');
  // A create whose outcome is unknown may still land, and a second POST would be a second
  // operation, so the control stays off until the Device page is opened again.
  const [uncertain, setUncertain] = useState(false);
  const hintID = useId();
  useEffect(() => () => scope.current.invalidate(), []);
  // As in the location editor: leaving the page, a new session or navigation, or a changed
  // Device ends the open request, so a confirmation never names a state the page no longer shows.
  useLayoutEffect(() => {
    const invalidate = () => scope.current.invalidate();
    const visibility = () => { if (document.visibilityState === 'hidden') invalidate(); };
    window.addEventListener('pagehide', invalidate);
    document.addEventListener('visibilitychange', visibility);
    return () => { window.removeEventListener('pagehide', invalidate); document.removeEventListener('visibilitychange', visibility); };
  }, []);
  useLayoutEffect(() => {
    scope.current.invalidate();
    setOpen(false); setAction(''); setReason(''); setConfirming(false);
  }, [device.metadata.uid, device.metadata.resourceVersion, session, generation]);
  if (!permitted) return null;
  const server = displayLabel(device);
  const offered = offeredActions(device);
  const block = powerBlock(device, { custody: custody.data, custodyError: custody.error, operations: operations.data })
    ?? (caller.error ? 'Your username and UID could not be read' : !caller.data || !ready || permission.isFetching ? 'Checking access…' : undefined)
    ?? (operations.error ? 'Power operations unreadable' : undefined)
    ?? (uncertain ? 'Outcome unknown · check Operations, then reopen this Device' : undefined);
  const chosen = offered.find(item => item === action);
  const dirty = open && (!!action || !!reason);
  function reset() { setAction(''); setReason(''); setConfirming(false); }
  function dismiss() {
    if (pending) return;
    if (dirty && !uncertain && !window.confirm('Discard this power request?')) return;
    scope.current.invalidate(); setOpen(false); reset();
  }
  async function send() {
    if (!chosen || !caller.data || pending || block) return;
    const operation = scope.current.begin(session);
    setPending(true); setMessage('');
    try {
      await createPowerOperation(device, chosen, reason, caller.data, operation);
      setOpen(false); reset(); setMessage(`${ACTION_WORDS[chosen]} requested`);
    } catch (error) {
      const failure = failureMessage(error, operation.sent);
      setMessage(failure.message);
      if (failure.uncertain) { setUncertain(true); setConfirming(false); }
    } finally {
      setPending(false);
      await client.invalidateQueries({ queryKey: POWER_QUERY_KEY });
    }
  }
  function submit(event: FormEvent) {
    event.preventDefault();
    if (!chosen || block) return;
    if (destructive(chosen) && !confirming) { setConfirming(true); return; }
    void send();
  }
  const hint = block ? { id: hintID, text: block } : undefined;
  const status = <p role="status" className="inventory-management-status">{message}</p>;
  const verb = chosen ? ACTION_WORDS[chosen] : 'Continue';
  return <section className="inventory-management inventory-power-control" aria-label="Power">
    {open && <InventoryDraftGuard dirty={dirty} pending={pending} uncertain={false} />}
    <InventoryFormSurface open={open} title={`Power · ${server}`} status={status} hint={hint} onDismiss={dismiss}
      trigger={<Button type="secondary" theme="outline" size="small" htmlType="button" disabled={!!block} aria-describedby={hint?.id}
        onClick={() => { if (block) return; scope.current = new InventoryOperationScope(); reset(); setMessage(''); setOpen(true); }}>Power</Button>}>
      <InventoryForm onSubmit={submit} message={uncertain ? message : ''}>
        <fieldset className="inventory-form-gate" disabled={pending || confirming || !!block}>
          <Label>Action<FieldSelect required value={action} onValueChange={setAction} placeholder="Choose an action" options={offered.map(item => ({ value: item, label: ACTION_WORDS[item] }))} /></Label>
          <Label>Reason<Input required maxLength={512} value={reason} onChange={event => setReason(event.target.value)} /></Label>
        </fieldset>
        {confirming && chosen && <p className="inventory-power-confirm" role="alert" title={`${device.metadata.name} · system ${device.spec.identity.systemUUID}`}>
          <strong>{ACTION_WORDS[chosen]} {server}?</strong>
        </p>}
        <div className="inventory-form-actions">
          <Button type="secondary" theme="outline" size="small" htmlType="button" disabled={pending} onClick={confirming ? () => setConfirming(false) : dismiss}>{confirming ? 'Back' : 'Cancel'}</Button>
          <Button type={confirming ? 'danger' : 'primary'} theme="solid" size="small" htmlType="submit" disabled={pending || !!block}>
            {pending ? 'Sending…' : confirming ? `${verb} ${server}` : chosen && destructive(chosen) ? 'Continue' : verb}
          </Button>
        </div>
      </InventoryForm>
    </InventoryFormSurface>
  </section>;
}

const HISTORY_LIMIT = 20;

function Time({ value, now }: { value?: string; now: number }) {
  const at = value ? Date.parse(value) : NaN;
  if (!Number.isFinite(at)) return null;
  return <time dateTime={value} title={value}>{formatAge((now - at) / 1000)}</time>;
}

function historyRow(operation: DevicePowerOperation, custody: DeviceCustody | null | undefined, now: number) {
  const evidence = operation.status?.evidence;
  const change = powerChange(evidence);
  const note = evidenceNote(evidence);
  const reason = operation.spec.authority.serviceRequest?.reason;
  return [
    <Time value={operation.metadata.creationTimestamp} now={now} />,
    ACTION_WORDS[operation.spec.action as PowerAction] ?? operation.spec.action,
    <PowerOutcomeBadge outcome={powerOutcome(operation, custody)} />,
    change ? <span className="inventory-numeric" title={note}>{change}{note && <span className="inventory-visually-hidden">. {note}</span>}</span> : null,
    <span title={operation.spec.creator.username}>{operation.spec.authority.allocationRef ? 'Metal provider' : operation.spec.creator.username}</span>,
    reason && <span className="inventory-power-reason" title={reason}>{reason}</span>,
  ];
}

/** Recent power operations for the Device, newest first. */
export function DevicePowerHistory({ device }: { device: Device }) {
  const operations = usePowerOperations(device);
  const custody = useCustody(device);
  if (operations.error) return <p className="inventory-muted">{operations.error.status === 403 ? 'Power operations unreadable' : 'Power operations could not be read'}</p>;
  if (!operations.data) return <p className="inventory-muted">Loading…</p>;
  if (!operations.data.length) return <p className="inventory-muted">No power operations</p>;
  const now = Date.now();
  return <SmallTable label="Power operations" head={['Time', 'Action', 'Result', 'Power', 'Creator', 'Reason']}
    rows={operations.data.slice(0, HISTORY_LIMIT).map(operation => historyRow(operation, custody.data, now))} />;
}

/** The Operations tab's count: the number of operations, or "?" when unreadable. */
export function usePowerTabCount(device: Device | undefined): { count?: number | string; countNote?: string } {
  const operations = usePowerOperations(device);
  if (operations.error) return { count: '?', countNote: 'Power operations unreadable' };
  return { count: operations.data?.length };
}
