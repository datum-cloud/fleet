import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Device, DeviceCustody, DevicePowerOperation } from '../../../shared/fleet-api';
import { FIXTURE_NAMES, inventoryFixture } from '../lib/inventory-fixtures';
import { fixtureCustody, fixturePowerOperations } from '../lib/power-fixtures';
import { chooseValue, selectOptions } from '../test/field-select';
import { InventoryOperationContext, InventoryOperationScope } from '../lib/inventory-operation';
import { DevicePowerControl, DevicePowerFact, DevicePowerHistory } from './device-power';

const named = (name: string) => structuredClone(inventoryFixture.devices.find(device => device.metadata.name === name)!);
const SERVER = FIXTURE_NAMES.server;
const UUID = named(SERVER).spec.identity.systemUUID!;
const NAME = named(SERVER).spec.displayName!;

interface Api {
  allowed?: boolean;
  custody?: DeviceCustody | null | number;
  operations?: DevicePowerOperation[] | number;
  caller?: { username: string; uid: string } | number;
  create?: (body: unknown) => Response | Promise<Response>;
}

const json = (data: unknown, status = 200) => Response.json(status < 400 ? { data } : { error: typeof data === 'string' ? data : 'Failed' }, { status });

function stub(api: Api = {}) {
  const { allowed = true, custody = fixtureCustody(SERVER), operations = [], caller = { username: 'staff@example.com', uid: 'staff-uid' } } = api;
  const fetch = vi.fn(async (url: string, init: RequestInit = {}) => {
    if (url.includes('selfsubjectaccessreviews')) return json({ status: { allowed } });
    if (url.includes('selfsubjectreviews')) return typeof caller === 'number' ? json('Failed', caller) : json({ status: { userInfo: caller } });
    if (url.includes('/devicecustodies/')) return typeof custody === 'number' ? json('Failed', custody) : custody === null ? json('Not found', 404) : json(custody);
    if (url.includes('/devicepoweroperations') && init.method === 'POST') return api.create ? api.create(JSON.parse(String(init.body))) : json({ ...JSON.parse(String(init.body)), metadata: { name: 'created', uid: 'created-uid' } }, 201);
    if (url.includes('/devicepoweroperations')) return typeof operations === 'number' ? json('Failed', operations) : json({ items: operations });
    throw new Error(`Unexpected request ${url}`);
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function show(device: Device, ui: 'control' | 'history' | 'fact' = 'control') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const node = ui === 'control' ? <DevicePowerControl device={device} /> : ui === 'history' ? <DevicePowerHistory device={device} /> : <dl><DevicePowerFact device={device} /></dl>;
  return { ...render(<QueryClientProvider client={client}>{node}</QueryClientProvider>), client };
}

const powerButton = () => screen.findByRole('button', { name: 'Power' });
async function enabledPower() {
  const button = await powerButton();
  await waitFor(() => expect(button).toBeEnabled());
  return button;
}
async function openDialog() {
  fireEvent.click(await enabledPower());
  return screen.getByRole('dialog');
}
const actionField = () => screen.getByRole('combobox', { name: /^Action/ });
const posts = (fetch: ReturnType<typeof stub>) => fetch.mock.calls.filter(([url, init]) => url.includes('/devicepoweroperations') && init?.method === 'POST');

beforeEach(() => vi.stubGlobal('confirm', vi.fn(() => true)));
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe('Power control', () => {
  it('offers only the actions discovery reports, in menu order', async () => {
    stub();
    const device = named(SERVER);
    device.status!.discovery!.inventory!.systems![0].resetTypes = ['PowerCycle', 'GracefulShutdown', 'On', 'Nmi'];
    show(device);
    await openDialog();
    expect(selectOptions(actionField()).map(option => [option.value, option.text])).toEqual([['On', 'Power on'], ['PowerCycle', 'Power cycle']]);
  });

  it('reads offered actions from the system with the recorded UUID, not another one', async () => {
    stub();
    const device = named(SERVER);
    const inventory = device.status!.discovery!.inventory!;
    inventory.systems = [{ id: 'Other', uuid: '00000000-0000-4000-8000-00000000ffff', resetTypes: ['On', 'ForceOff', 'ForceRestart', 'PowerCycle'] }, { ...inventory.systems![0], uuid: UUID.toUpperCase(), resetTypes: ['ForceOff'] }];
    show(device);
    await openDialog();
    expect(selectOptions(actionField()).map(option => option.value)).toEqual(['ForceOff']);
  });

  it('requires a reason and sends nothing without one', async () => {
    const fetch = stub();
    show(named(SERVER));
    await openDialog();
    chooseValue(actionField(), 'On');
    fireEvent.click(screen.getByRole('button', { name: 'Power on' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/Reason/);
    expect(screen.getByRole('textbox', { name: 'Reason' })).toHaveAttribute('aria-invalid', 'true');
    expect(posts(fetch)).toHaveLength(0);
  });

  it('sends the exact staff request: generated name, device-uid label, caller, Device name, reason and recorded system UUID', async () => {
    const fetch = stub();
    const device = named(SERVER);
    show(device);
    await openDialog();
    chooseValue(actionField(), 'On');
    fireEvent.change(screen.getByRole('textbox', { name: 'Reason' }), { target: { value: '  First power-on after racking  ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Power on' }));
    await waitFor(() => expect(posts(fetch)).toHaveLength(1));
    const [url, init] = posts(fetch)[0];
    expect(url).toBe('/api/internal/apis/resourcemanager.miloapis.com/v1alpha1/projects/datum-cloud/control-plane/apis/fleet.datumapis.com/v1alpha1/namespaces/default/devicepoweroperations');
    expect(JSON.parse(String(init!.body))).toEqual({
      apiVersion: 'fleet.datumapis.com/v1alpha1',
      kind: 'DevicePowerOperation',
      metadata: { generateName: `${SERVER}-power-`, namespace: 'default', labels: { 'fleet.datumapis.com/device-uid': device.metadata.uid } },
      spec: {
        // The creator is the only object UID a spec may carry; the Device incarnation is the label.
        creator: { username: 'staff@example.com', uid: 'staff-uid' },
        deviceRef: { name: SERVER },
        authority: { serviceRequest: { reason: 'First power-on after racking' } },
        action: 'On',
        expectedSystemUUID: UUID,
      },
    });
    expect(await screen.findByRole('status')).toHaveTextContent('Power on requested');
    // The system UUID comes from the record: the dialog has no field for it.
    expect(screen.queryByRole('textbox', { name: /UUID/ })).toBeNull();
  });

  it.each([['ForceOff', 'Force off'], ['ForceRestart', 'Force restart'], ['PowerCycle', 'Power cycle']])('asks again before %s, naming the server', async (action, words) => {
    const fetch = stub();
    show(named(SERVER));
    await openDialog();
    chooseValue(actionField(), action);
    fireEvent.change(screen.getByRole('textbox', { name: 'Reason' }), { target: { value: 'Hung in firmware' } });
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    expect(posts(fetch)).toHaveLength(0);
    expect(screen.getByRole('alert')).toHaveTextContent(`${words} ${NAME}?`);
    // Back returns to the form without sending.
    fireEvent.click(screen.getByRole('button', { name: 'Back' }));
    expect(screen.queryByText(`${words} ${NAME}?`)).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    fireEvent.click(screen.getByRole('button', { name: `${words} ${NAME}` }));
    await waitFor(() => expect(posts(fetch)).toHaveLength(1));
    expect(JSON.parse(String(posts(fetch)[0][1]!.body)).spec.action).toBe(action);
  });

  it('powers on without a second question', async () => {
    const fetch = stub();
    show(named(SERVER));
    await openDialog();
    chooseValue(actionField(), 'On');
    fireEvent.change(screen.getByRole('textbox', { name: 'Reason' }), { target: { value: 'Racked' } });
    fireEvent.click(screen.getByRole('button', { name: 'Power on' }));
    await waitFor(() => expect(posts(fetch)).toHaveLength(1));
  });

  it('is hidden without the power operator permission', async () => {
    const fetch = stub({ allowed: false });
    show(named(SERVER));
    await waitFor(() => expect(fetch.mock.calls.some(([url]) => url.includes('selfsubjectaccessreviews'))).toBe(true));
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
    expect(screen.queryByRole('button', { name: 'Power' })).toBeNull();
    const review = JSON.parse(String(fetch.mock.calls.find(([url]) => url.includes('selfsubjectaccessreviews'))![1]!.body));
    expect(review.spec.resourceAttributes).toEqual({ group: 'fleet.datumapis.com', version: 'v1alpha1', resource: 'devicepoweroperations', namespace: 'default', verb: 'create' });
    // A user who cannot create is never asked for their identity.
    expect(fetch.mock.calls.some(([url]) => url.includes('selfsubjectreviews'))).toBe(false);
  });

  const custodyWith = (spec: Partial<DeviceCustody['spec']>) => ({ ...fixtureCustody(SERVER)!, spec: { ...fixtureCustody(SERVER)!.spec, ...spec } });
  const running = { ...fixturePowerOperations[0], status: { phase: 'Dispatched' as const } };
  it.each<[string, (device: Device) => void, Api]>([
    ['Allocated to a customer', () => undefined, { custody: custodyWith({ holder: { type: 'Allocation', allocationRef: { name: 'example-allocation' } } }) }],
    ['Allocated to a customer', () => undefined, { custody: custodyWith({ holder: { type: 'Release', allocationRef: { name: 'example-allocation' }, openGates: ['Cleaning'] } }) }],
    ['Held: DevicePowerOperation example-held outcome unknown', () => undefined, { custody: custodyWith({ holder: { type: 'Service' }, recoveryHold: { kind: 'DevicePowerOperation', name: 'example-held', fence: 3, since: '2026-01-02T03:04:05Z', reason: 'SiteCouldNotProve' } }) }],
    ['DeviceWipe example-wipe is running', () => undefined, { custody: custodyWith({ activeOperation: { kind: 'DeviceWipe', name: 'example-wipe', fence: 5, phase: 'Dispatched', admittedAt: '', deadline: '', creator: { username: 'release', uid: 'r' } } }) }],
    ['Force restart is in progress', () => undefined, { operations: [running] }],
    ['Custody unverified', () => undefined, { custody: custodyWith({ state: 'Unverified' }) }],
    ['Retired', () => undefined, { custody: custodyWith({ holder: { type: 'Retired' } }) }],
    ['No custody record', () => undefined, { custody: null }],
    ['No custody record', () => undefined, { custody: { ...fixtureCustody(SERVER)!, metadata: { ...fixtureCustody(SERVER)!.metadata, labels: { 'fleet.datumapis.com/device-uid': 'removed-device-uid' } } } }],
    ['No custody record', () => undefined, { custody: { ...fixtureCustody(SERVER)!, metadata: { ...fixtureCustody(SERVER)!.metadata, labels: undefined } } }],
    ['Custody unreadable', () => undefined, { custody: 403 }],
    ['Not discovered', device => { device.status = undefined; }, {}],
    ['No system UUID recorded', device => { device.spec.identity.systemUUID = undefined; }, {}],
    ['The BMC offers no power actions', device => { device.status!.discovery!.inventory!.systems![0].resetTypes = undefined; }, {}],
    ['Discovery read another BMC endpoint', device => { device.status!.discovery!.inventory!.endpoint = 'https://192.0.2.99:443'; }, {}],
    ['Your username and UID could not be read', () => undefined, { caller: 500 }],
    ['Power operations unreadable', () => undefined, { operations: 403 }],
    ['Identity not matched', device => { device.status!.conditions = device.status!.conditions!.map(item => item.type === 'IdentityMatched' ? { ...item, status: 'False', reason: 'SerialMismatch' } : item); }, {}],
    ['Identity not matched', device => { device.metadata.generation = 2; }, {}],
    ['BMC key not read', device => { device.status!.discovery!.tls = undefined; }, {}],
    ['BMC key not read', device => { device.status!.discovery!.tls!.endpoint = 'https://192.0.2.99:443'; }, {}],
    ['BMC key shared', device => { device.status!.conditions = device.status!.conditions!.map(item => item.type === 'PublicKeyUnique' ? { ...item, status: 'False', reason: 'Shared' } : item); }, {}],
    ['Held for a staff operation', () => undefined, { custody: custodyWith({ holder: { type: 'Service' } }) }],
  ])('is disabled with the reason as its tooltip: %s', async (reason, change, api) => {
    const fetch = stub(api);
    const device = named(SERVER);
    change(device);
    show(device);
    const button = await powerButton();
    await waitFor(() => expect(button).toHaveAccessibleDescription(reason));
    expect(button).toBeDisabled();
    expect(button.closest('[title]')).toHaveAttribute('title', reason);
    fireEvent.click(button);
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(posts(fetch)).toHaveLength(0);
  });

  it('is not blocked by a free Device with disabled capacity: only custody decides', async () => {
    stub({ custody: custodyWith({ holder: { type: 'None' } }) });
    show(named(SERVER));
    expect(await enabledPower()).not.toHaveAccessibleDescription();
  });

  it('keeps the control off after an unknown outcome, even after operations are read again', async () => {
    const fetch = stub({ create: () => json('Upstream timeout', 504) });
    const { client } = show(named(SERVER));
    await openDialog();
    chooseValue(actionField(), 'On');
    fireEvent.change(screen.getByRole('textbox', { name: 'Reason' }), { target: { value: 'Racked' } });
    fireEvent.click(screen.getByRole('button', { name: 'Power on' }));
    await waitFor(() => expect(screen.getAllByRole('status')[0]).toHaveTextContent('Outcome unknown. Check Operations before another request.'));
    expect(posts(fetch)).toHaveLength(1);
    // The submit is off until the list has been reread: a second POST would be a second operation.
    expect(screen.getByRole('button', { name: 'Power on' })).toBeDisabled();
    fireEvent.submit(screen.getByRole('button', { name: 'Power on' }).closest('form')!);
    expect(posts(fetch)).toHaveLength(1);
    // A reread does not prove the create did not land: it may still be in flight.
    await act(async () => { await client.refetchQueries(); });
    expect(screen.getByRole('button', { name: 'Power on' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.getByRole('button', { name: 'Power' })).toHaveAccessibleDescription('Outcome unknown · check Operations, then reopen this Device');
    expect(posts(fetch)).toHaveLength(1);
  });

  it('keeps polling after a failed read, so one blip does not leave the control off', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      let fail = true;
      const fetch = stub();
      const base = fetch.getMockImplementation()!;
      fetch.mockImplementation(async (url: string, init?: RequestInit) => url.includes('/devicepoweroperations') && init?.method !== 'POST' && fail ? json('Unavailable', 503) : base(url, init));
      show(named(SERVER));
      const button = await powerButton();
      await waitFor(() => expect(button).toHaveAccessibleDescription('Power operations unreadable'));
      fail = false;
      await act(async () => { await vi.advanceTimersByTimeAsync(11_000); });
      await waitFor(() => expect(button).toBeEnabled());
    } finally { vi.useRealTimers(); }
  });

  it('closes an open request when the Device changes, and sends nothing for the old one', async () => {
    const fetch = stub();
    const device = named(SERVER);
    const view = show(device);
    await openDialog();
    chooseValue(actionField(), 'ForceOff');
    fireEvent.change(screen.getByRole('textbox', { name: 'Reason' }), { target: { value: 'Move' } });
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    view.rerender(<QueryClientProvider client={view.client}><DevicePowerControl device={{ ...device, metadata: { ...device.metadata, resourceVersion: 'changed' } }} /></QueryClientProvider>);
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(posts(fetch)).toHaveLength(0);
  });

  it('says nothing was sent when the request fails before the POST, and stays usable', async () => {
    const fetch = stub();
    const base = fetch.getMockImplementation()!;
    let reviews = 0;
    // The permission is checked again at submit; that check fails on the network.
    fetch.mockImplementation(async (url: string, init?: RequestInit) => url.includes('selfsubjectaccessreviews') && ++reviews > 1 ? Promise.reject(new TypeError('network')) : base(url, init));
    show(named(SERVER));
    await openDialog();
    chooseValue(actionField(), 'On');
    fireEvent.change(screen.getByRole('textbox', { name: 'Reason' }), { target: { value: 'Racked' } });
    fireEvent.click(screen.getByRole('button', { name: 'Power on' }));
    await waitFor(() => expect(within(screen.getByRole('dialog')).getByRole('status')).toHaveTextContent('Nothing was sent. Try again.'));
    expect(posts(fetch)).toHaveLength(0);
    expect(screen.getByRole('button', { name: 'Power on' })).toBeEnabled();
  });

  it('reports a session change before sending as nothing sent', async () => {
    const fetch = stub();
    const session = new InventoryOperationScope();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<InventoryOperationContext.Provider value={session}><QueryClientProvider client={client}><DevicePowerControl device={named(SERVER)} /></QueryClientProvider></InventoryOperationContext.Provider>);
    await openDialog();
    chooseValue(actionField(), 'On');
    fireEvent.change(screen.getByRole('textbox', { name: 'Reason' }), { target: { value: 'Racked' } });
    session.invalidate();
    fireEvent.click(screen.getByRole('button', { name: 'Power on' }));
    await waitFor(() => expect(within(screen.getByRole('dialog')).getByRole('status')).toHaveTextContent('The session changed. Nothing was sent.'));
    expect(posts(fetch)).toHaveLength(0);
  });

  it('reports a denied create without calling it unknown', async () => {
    stub({ create: () => json('forbidden', 403) });
    show(named(SERVER));
    await openDialog();
    chooseValue(actionField(), 'On');
    fireEvent.change(screen.getByRole('textbox', { name: 'Reason' }), { target: { value: 'Racked' } });
    fireEvent.click(screen.getByRole('button', { name: 'Power on' }));
    await waitFor(() => expect(within(screen.getByRole('dialog')).getByRole('status')).toHaveTextContent('Power operator access is no longer valid. Nothing was created.'));
    expect(screen.getByRole('button', { name: 'Power on' })).toBeEnabled();
  });
});

describe('Operations history', () => {
  const operation = (name: string, phase: NonNullable<DevicePowerOperation['status']>['phase'], extra: Partial<NonNullable<DevicePowerOperation['status']>> = {}, created = '2026-01-02T03:04:05Z'): DevicePowerOperation => ({
    metadata: { name, namespace: 'default', uid: `${name}-uid`, creationTimestamp: created, labels: { 'fleet.datumapis.com/device-uid': named(SERVER).metadata.uid! } },
    spec: { creator: { username: 'staff@example.com', uid: 's' }, deviceRef: { name: SERVER }, authority: { serviceRequest: { reason: `Reason for ${name}` } }, action: 'ForceRestart', expectedSystemUUID: UUID },
    status: { phase, ...extra },
  });
  const rows = () => within(screen.getByRole('table', { name: 'Power operations' })).getAllByRole('row').slice(1);
  const resultOf = (name: string) => rows().find(row => row.textContent?.includes(`Reason for ${name}`))!.querySelector('.inventory-power-outcome')!;

  it.each<[string, DevicePowerOperation['status'], string, string | undefined]>([
    ['Pending', { phase: 'Pending' }, 'Pending', 'progress'],
    ['Admitted', { phase: 'Admitted' }, 'Admitted', 'progress'],
    ['Dispatched', { phase: 'Dispatched' }, 'Sent', 'progress'],
    ['Verifying', { phase: 'Verifying' }, 'Verifying', 'progress'],
    ['Succeeded', { phase: 'Succeeded', evidence: { requestSentAt: '2026-01-02T03:04:06Z', power: { before: { state: 'On', observedAt: '2026-01-02T03:04:05Z' }, after: { state: 'On', observedAt: '2026-01-02T03:04:41Z' } }, resetEvidence: 'LastResetTime' } }, 'Succeeded', undefined],
    ['refused', { phase: 'Failed', reason: 'SystemUUIDMismatch', message: 'expectedSystemUUID is not the recorded system UUID' }, 'Refused', 'danger'],
    ['failed after sending', { phase: 'Failed', reason: 'BMCRefused', evidence: { requestSentAt: '2026-01-02T03:04:06Z' } }, 'Failed', 'danger'],
    ['Cancelled Expired', { phase: 'Cancelled', reason: 'Expired', message: 'a staff operation must be admitted within 5m0s' }, 'Expired', 'warning'],
    ['Cancelled', { phase: 'Cancelled', reason: 'Withdrawn' }, 'Cancelled', 'warning'],
    ['OutcomeUnknown', { phase: 'OutcomeUnknown', reason: 'SiteCouldNotProve' }, 'Outcome unknown', 'danger'],
  ])('shows %s with its tone and reason', async (_, status, label, tone) => {
    stub({ operations: [operation('one', status!.phase!, status)] });
    show(named(SERVER), 'history');
    const result = await waitFor(() => resultOf('one'));
    expect(result).toHaveTextContent(new RegExp(`^${label}`));
    expect(result.getAttribute('data-tone') ?? undefined).toBe(tone);
    const reason = [status!.reason, status!.message].filter(Boolean).join(' · ');
    if (reason) expect(result).toHaveAttribute('title', reason);
  });

  const holdOn = (name: string, fence: number) => ({ ...fixtureCustody(SERVER)!, spec: { ...fixtureCustody(SERVER)!.spec, holder: { type: 'Service' as const }, recoveryHold: { kind: 'DevicePowerOperation', name, fence, since: '2026-01-02T03:10:00Z', reason: 'SiteCouldNotProve' } } });

  it('names the recovery hold on an unknown outcome custody holds, matched by kind, name and admission fence', async () => {
    const held = operation('held', 'OutcomeUnknown', { reason: 'SiteCouldNotProve', admission: { fence: 3 } });
    stub({ operations: [held], custody: holdOn('held', 3) });
    show(named(SERVER), 'history');
    await waitFor(() => expect(resultOf('held')).toHaveTextContent('Outcome unknown · held'));
    expect(resultOf('held')).toHaveAttribute('title', 'SiteCouldNotProve\nRecovery hold since 2026-01-02T03:10:00Z');
  });

  it.each<[string, Partial<NonNullable<DevicePowerOperation['status']>>]>([
    ['a same-name operation created later, with no admission', {}],
    ['a same-name operation admitted at another fence', { admission: { fence: 9 } }],
  ])('does not claim the hold for %s', async (_, admission) => {
    stub({ operations: [operation('held', 'OutcomeUnknown', { reason: 'SiteCouldNotProve', ...admission })], custody: holdOn('held', 3) });
    show(named(SERVER), 'history');
    await waitFor(() => expect(resultOf('held')).toHaveTextContent(/^Outcome unknown\. SiteCouldNotProve$/));
    expect(resultOf('held')).not.toHaveTextContent(/held/i);
  });

  it('does not claim a hold for an unknown outcome custody no longer holds', async () => {
    stub({ operations: [operation('settled', 'OutcomeUnknown', { reason: 'SiteCouldNotProve' })] });
    show(named(SERVER), 'history');
    await waitFor(() => expect(resultOf('settled')).toHaveTextContent(/^Outcome unknown\. SiteCouldNotProve$/));
    expect(resultOf('settled')).not.toHaveTextContent(/held/i);
  });

  it('lists action, result, power before and after, creator and reason, newest first', async () => {
    stub({ operations: fixturePowerOperations.filter(item => item.spec.deviceRef.name === SERVER).reverse() });
    show(named(SERVER), 'history');
    await waitFor(() => expect(rows()).toHaveLength(4));
    expect(within(screen.getByRole('table')).getAllByRole('columnheader').map(cell => cell.textContent)).toEqual(['Time', 'Action', 'Result', 'Power', 'Creator', 'Reason']);
    const cells = rows().map(row => within(row).getAllByRole('cell').slice(1).map(cell => cell.textContent));
    expect(cells[0]).toEqual(['Force restart', 'Succeeded', expect.stringMatching(/^On → On\./), 'staff@example.com', 'Hung in firmware during qualification']);
    expect(cells[1]).toEqual(['Power cycle', expect.stringMatching(/^Refused/), '', 'tech@example.com', 'Retry after the BMC replacement']);
    expect(cells[2]).toEqual(['Force off', expect.stringMatching(/^Expired/), '', 'tech@example.com', 'Move to rack a02']);
    expect(cells[3]).toEqual(['Power on', 'Succeeded', expect.stringMatching(/^Off → On\./), 'staff@example.com', 'First power-on after racking']);
    expect(within(rows()[0]).getByText('On → On')).toHaveAttribute('title', expect.stringContaining('BMC reported a reset'));
  });

  it('lists by the device-uid label, and ignores operations for another Device of the same name', async () => {
    const old = operation('old', 'Succeeded');
    const fetch = stub({ operations: [{ ...old, metadata: { ...old.metadata, labels: { 'fleet.datumapis.com/device-uid': 'removed-device-uid' } } }] });
    show(named(SERVER), 'history');
    expect(await screen.findByText('No power operations')).toBeInTheDocument();
    const list = fetch.mock.calls.find(([url, init]) => url.includes('/devicepoweroperations') && init?.method !== 'POST')![0];
    expect(new URL(list, 'http://x').searchParams.get('labelSelector')).toBe(`fleet.datumapis.com/device-uid=${named(SERVER).metadata.uid}`);
  });

  it('ignores an unlabelled operation', async () => {
    const bare = operation('bare', 'Succeeded');
    stub({ operations: [{ ...bare, metadata: { ...bare.metadata, labels: undefined } }] });
    show(named(SERVER), 'history');
    expect(await screen.findByText('No power operations')).toBeInTheDocument();
  });

  it('says the history is unreadable rather than empty', async () => {
    stub({ operations: 403 });
    show(named(SERVER), 'history');
    expect(await screen.findByText('Power operations unreadable')).toBeInTheDocument();
  });
});

describe('header power fact', () => {
  it('shows the observed state and the running operation', async () => {
    stub({ operations: [{ ...fixturePowerOperations[0], status: { phase: 'Verifying' } }] });
    show(named(SERVER), 'fact');
    await waitFor(() => expect(screen.getByRole('definition')).toHaveTextContent('On Force restart · Verifying'));
  });

  it('drops an operation that ended over an hour ago', async () => {
    stub({ operations: [{ ...fixturePowerOperations[0], status: { phase: 'Succeeded', finishedAt: new Date(Date.now() - 2 * 3_600_000).toISOString() } }] });
    show(named(SERVER), 'fact');
    await waitFor(() => expect(screen.getByRole('definition')).toHaveTextContent(/^On$/));
  });

  it('keeps an unknown outcome while custody holds it', async () => {
    const held = { ...fixturePowerOperations[0], status: { phase: 'OutcomeUnknown' as const, admission: { fence: 2 }, finishedAt: new Date(Date.now() - 5 * 3_600_000).toISOString() } };
    stub({ operations: [held], custody: { ...fixtureCustody(SERVER)!, spec: { ...fixtureCustody(SERVER)!.spec, recoveryHold: { kind: 'DevicePowerOperation', name: held.metadata.name, fence: 2, since: held.status.finishedAt, reason: 'SiteCouldNotProve' } } } });
    show(named(SERVER), 'fact');
    await waitFor(() => expect(screen.getByRole('definition')).toHaveTextContent('On Force restart · Outcome unknown · held'));
  });
});
