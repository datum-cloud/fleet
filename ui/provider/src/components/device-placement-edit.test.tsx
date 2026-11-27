import { chooseValue } from '../test/field-select';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import type { Device, PhysicalRecord } from '../../../shared/fleet-api';
import { fixturePhysicalDevices } from '../lib/fixtures';
import { DevicePlacementEdit } from './device-placement-edit';
import { useInventoryPermission } from '../lib/inventory-management';
import { InventoryOperationContext, InventoryOperationScope } from '../lib/inventory-operation';

vi.mock('../lib/inventory-management', async () => ({ ...await vi.importActual('../lib/inventory-management'), useInventoryPermission: vi.fn() }));
const permission = vi.mocked(useInventoryPermission);
const device: Device = { ...fixturePhysicalDevices[0], metadata: { name: 'example', namespace: 'default', uid: 'uid', resourceVersion: '7' }, spec: { ...fixturePhysicalDevices[0].spec, reportedPlacement: undefined } };
const rack: PhysicalRecord = { kind: 'Rack', metadata: { name: 'rack', namespace: 'default', uid: 'rack-uid' }, spec: { displayName: 'Restricted rack name', heightUnits: 42 } };
const response = (data: unknown, status = 200) => ({ ok: status < 400, status, json: async () => ({ data }) });
function setup(record = device, session = new InventoryOperationScope()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(<InventoryOperationContext.Provider value={session}><QueryClientProvider client={client}><DevicePlacementEdit device={record} locations={[rack]} /></QueryClientProvider></InventoryOperationContext.Provider>);
  return { ...view, client, session };
}
function openAndFill(existing = false) {
  fireEvent.click(screen.getByRole('button', { name: existing ? 'Correct location' : 'Add reported location' }));
  fireEvent.change(screen.getByLabelText('Correction reason'), { target: { value: 'Correct recorded location' } });
  fireEvent.change(screen.getByLabelText('Evidence reference'), { target: { value: 'ticket/example' } });
}
beforeEach(() => { vi.clearAllMocks(); permission.mockReturnValue({ data: true, isFetching: false, error: null } as never); });
beforeEach(() => vi.stubGlobal('confirm', vi.fn(() => true)));

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe('reported location editor', () => {
  it('picks the observation time with the datum-ui date-time picker, defaulting to when the form opened', () => {
    vi.stubGlobal('fetch', vi.fn());
    setup(); openAndFill();
    expect(screen.queryByRole('textbox', { name: /Observ/ })).not.toBeInTheDocument();
    const observed = screen.getByRole('combobox', { name: /^Observed at/ });
    expect(observed).toHaveTextContent(String(new Date().getFullYear()));
    expect(observed).toBeEnabled();
  });
  describe('observation time', () => {
    // A fixed local noon, so an earlier and a later time on the same day are both known.
    const now = new Date(2026, 2, 10, 12, 0);
    beforeEach(() => { vi.useFakeTimers({ toFake: ['Date'] }); vi.setSystemTime(now); });
    afterEach(() => vi.useRealTimers());
    function pickTime(time: string) {
      fireEvent.click(screen.getByRole('combobox', { name: /^Observed at/ }));
      fireEvent.change(screen.getByRole('textbox', { name: 'Select time' }), { target: { value: time } });
      fireEvent.click(screen.getByRole('button', { name: 'Apply' }));
    }
    it('submits the exact past time that was picked', async () => {
      const fetch = vi.fn().mockImplementation(async (_url: string, request: RequestInit) => {
        if (request.method === 'POST') return response({ status: { allowed: true } });
        if (request.method === 'PATCH') return response({ ...device, metadata: { ...device.metadata, resourceVersion: '8' }, spec: { ...device.spec, reportedPlacement: JSON.parse(request.body as string)[2].value } });
        return response(device);
      });
      vi.stubGlobal('fetch', fetch);
      setup(); openAndFill();
      pickTime('09:30');
      expect(screen.queryByText(/later than now/)).not.toBeInTheDocument();
      fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
      await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Location saved'));
      const patch = JSON.parse(fetch.mock.calls.find(([, request]) => request.method === 'PATCH')![1].body as string);
      expect(patch[2].value.observedAt).toBe(new Date(2026, 2, 10, 9, 30).toISOString());
    });
    it('flags a later time today at once and sends no PATCH', async () => {
      const fetch = vi.fn().mockResolvedValue(response({ status: { allowed: true } }));
      vi.stubGlobal('fetch', fetch);
      setup(); openAndFill();
      pickTime('15:45');
      const observed = screen.getByRole('combobox', { name: /^Observed at/ });
      expect(screen.getByRole('alert')).toHaveTextContent('Observation time cannot be later than now.');
      expect(observed).toHaveAttribute('aria-invalid', 'true');
      expect(observed).toHaveAccessibleDescription('Observation time cannot be later than now.');
      const save = screen.getByRole('button', { name: 'Save changes' });
      expect(save).toBeDisabled();
      fireEvent.submit(save.closest('form')!);
      await new Promise(done => setTimeout(done, 0));
      expect(fetch.mock.calls.some(([, request]) => request?.method === 'PATCH')).toBe(false);
      pickTime('11:15');
      expect(screen.queryByRole('alert')).not.toBeInTheDocument();
      expect(observed).not.toHaveAttribute('aria-invalid');
      expect(save).toBeEnabled();
    });
  });
  it.each([false, true])('adds/edits a Device reported location and refreshes projections: %s', async existing => {
    const record = structuredClone(device);
    if (existing) record.spec.reportedPlacement = { revision: 1, observedAt: '2026-01-01T00:00:00Z', reason: 'Synthetic survey', reference: 'ticket/synthetic', rackRef: { name: 'rack' }, position: 12, height: 4, face: 'rear', fullDepth: true };
    const fetch = vi.fn().mockImplementation(async (_url: string, request: RequestInit) => {
      if (request.method === 'POST') return response({ status: { allowed: true } });
      if (request.method === 'PATCH') return response({ ...record, metadata: { ...record.metadata, resourceVersion: '8' }, spec: { ...record.spec, reportedPlacement: JSON.parse(request.body as string)[2].value } });
      return response(_url.includes('/racks/') ? rack : record);
    });
    vi.stubGlobal('fetch', fetch);
    const { client } = setup(record);
    const invalidate = vi.spyOn(client, 'invalidateQueries');
    openAndFill(existing);
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Location saved'));
    expect(invalidate).toHaveBeenCalled();
    const patch = JSON.parse(fetch.mock.calls.find(([, request]) => request.method === 'PATCH')![1].body as string);
    expect(patch[2].value.revision).toBe(existing ? 2 : 1);
    expect(patch[2].value.height).toBe(existing ? 4 : undefined);
    expect(patch[2].value.fullDepth).toBe(existing ? true : undefined);
  });
  it.each([401, 403, 404, 409, 422, 503])('does not retry failed dispatch %s', async status => {
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(device)).mockResolvedValueOnce(response({}, status));
    vi.stubGlobal('fetch', fetch); setup(); openAndFill();
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/no longer valid|changed or disappeared|unknown/));
    expect(screen.getByRole('status')).not.toHaveTextContent('saved');
    expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled();
    expect(fetch).toHaveBeenCalledTimes(3);
    if (status === 503) expect(screen.getByRole('status')).toHaveTextContent('unknown');
  });
  it.each([401, 403, 404, 503])('purges denied rack references and choices: %s', async status => {
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(device)).mockResolvedValueOnce(response({}, status));
    vi.stubGlobal('fetch', fetch); setup(); openAndFill();
    chooseValue(screen.getByLabelText('Rack'), 'rack');
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Restricted names and choices were removed'));
    expect(screen.queryByText('Restricted rack name')).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue('rack')).not.toBeInTheDocument();
    expect(fetch).toHaveBeenCalledTimes(3);
  });
  it.each(['session', 'unmount', 'revalidation'])('revokes a delayed permission read on %s', async event => {
    let resolve!: (value: unknown) => void;
    const fetch = vi.fn().mockImplementationOnce(() => new Promise(done => { resolve = done; }));
    vi.stubGlobal('fetch', fetch);
    const view = setup(); openAndFill(); fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(resolve).toBeDefined());
    if (event === 'session') view.session.invalidate();
    if (event === 'hidden') {
      vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
      fireEvent(document, new Event('visibilitychange'));
    }
    if (event === 'unmount') view.unmount();
    if (event === 'revalidation') {
      permission.mockReturnValue({ data: true, isFetching: true, error: null } as never);
      view.rerender(<InventoryOperationContext.Provider value={view.session}><QueryClientProvider client={view.client}><DevicePlacementEdit device={device} locations={[rack]} /></QueryClientProvider></InventoryOperationContext.Provider>);
    }
    resolve(response({ status: { allowed: true } }));
    if (event !== 'unmount') await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('No write was dispatched'));
    else await new Promise(done => setTimeout(done, 0));
    expect(fetch).toHaveBeenCalledTimes(1);
  });
  it('freezes the original baseline instead of accepting a later render', async () => {
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch);
    const view = setup(); openAndFill();
    view.rerender(<InventoryOperationContext.Provider value={view.session}><QueryClientProvider client={view.client}><DevicePlacementEdit device={{ ...device, metadata: { ...device.metadata, resourceVersion: '8' } }} locations={[rack]} /></QueryClientProvider></InventoryOperationContext.Provider>);
    expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled();
    expect(fetch).not.toHaveBeenCalled();
  });
  it.each(['session', 'navigation'])('revokes a delayed reference read across %s', async event => {
    let resolve!: (value: unknown) => void;
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(device)).mockImplementationOnce(() => new Promise(done => { resolve = done; }));
    vi.stubGlobal('fetch', fetch);
    const view = setup(); openAndFill();
    chooseValue(screen.getByLabelText('Rack'), 'rack');
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(resolve).toBeDefined());
    if (event === 'session') view.session.invalidate();
    if (event === 'navigation') view.unmount();
    if (event === 'pagehide/pageshow') { fireEvent(window, new Event('pagehide')); fireEvent(window, new Event('pageshow')); }
    resolve(response(rack));
    if (event !== 'navigation') await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('No write was dispatched'));
    else await new Promise(done => setTimeout(done, 0));
    expect(fetch).toHaveBeenCalledTimes(3);
  });
  it('shows unknown, not success or retry, after a dispatched write loses authority', async () => {
    let resolve!: (value: unknown) => void;
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(device)).mockImplementationOnce(() => new Promise(done => { resolve = done; }));
    vi.stubGlobal('fetch', fetch);
    const view = setup(); openAndFill(); fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(resolve).toBeDefined());
    view.session.invalidate(); resolve(response(device));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('outcome is unknown'));
    expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled();
    expect(fetch).toHaveBeenCalledTimes(3);
    fireEvent.click(screen.getByRole('button', { name: 'Leave form' }));
    expect(screen.getByRole('button', { name: 'Add reported location' })).toBeDisabled();
  });
  it('disables editing for denied permission and GitOps-managed records', () => {
    permission.mockReturnValue({ data: false, isFetching: false, error: null } as never);
    setup({ ...device, metadata: { ...device.metadata, managedFields: [{ manager: 'kustomize-controller' }] } });
    expect(screen.getByRole('button', { name: 'Add reported location' })).toBeDisabled();
  });
});
