import { setField } from '../test/field-select';
import { chooseEndpoint } from '../test/cable-endpoint';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { DevicePlacementEdit } from './device-placement-edit';
import * as management from '../lib/inventory-management';
import { correctDevicePlacement } from '../lib/device-placement-edit';
import { ApiError } from '../lib/api';
import { fixturePhysicalDevices, fixturePhysicalRecords, fixturePhysicalClaims } from '../lib/fixtures';
import { InventoryOperationScope } from '../lib/inventory-operation';
import type { ManagedKind } from '../lib/inventory-management';
import { StrictMode } from 'react';

vi.mock('../lib/inventory-management', async () => ({ ...await vi.importActual('../lib/inventory-management'), useInventoryPermission: vi.fn(), createInventoryRecord: vi.fn(), editInventoryLabel: vi.fn() }));
vi.mock('../lib/device-placement-edit', async () => ({ ...await vi.importActual('../lib/device-placement-edit'), correctDevicePlacement: vi.fn() }));
const permission = vi.mocked(management.useInventoryPermission);
const create = vi.mocked(management.createInventoryRecord);
const edit = vi.mocked(management.editInventoryLabel);
const placement = vi.mocked(correctDevicePlacement);
const facilities = fixturePhysicalRecords.filter(record => record.kind === 'Facility').map(record => ({ ...record, metadata: { ...record.metadata, uid: 'facility-uid', resourceVersion: '1' } }));
const claim = { ...fixturePhysicalClaims[0], metadata: { ...fixturePhysicalClaims[0].metadata, uid: 'plan-uid', resourceVersion: '7' }, spec: { ...fixturePhysicalClaims[0].spec, lifecycle: 'Installation' as const } };
const device = { ...fixturePhysicalDevices[0], kind: 'Device' as const, metadata: { ...fixturePhysicalDevices[0].metadata, uid: 'device-uid', resourceVersion: '8', managedFields: [{ manager: management.INVENTORY_FIELD_MANAGER }] } };
const feeds = fixturePhysicalRecords.filter(record => record.kind === 'PowerFeed').map(record => ({ ...record, metadata: { ...record.metadata, uid: 'feed-uid', resourceVersion: '1' } }));

beforeEach(() => {
  vi.clearAllMocks();
  permission.mockReturnValue({ data: true, isFetching: false, error: null } as never);
  vi.stubGlobal('confirm', vi.fn(() => false));
});
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

function setup(kind: ManagedKind = 'Facility', record?: management.ManagedRecord, strict = false) {
  const task = !record && ['Device', 'Cable', 'DeviceClaim'].includes(kind);
  const router = createMemoryRouter([{ path: '/', element: <InventoryManagement kind={kind} task={task} record={record} facilities={facilities} claims={[claim]} devices={[device]} feeds={feeds} /> }, { path: '/elsewhere', element: <p>Other page</p> }]);
  const content = <QueryClientProvider client={new QueryClient()}><RouterProvider router={router} /></QueryClientProvider>;
  render(strict ? <StrictMode>{content}</StrictMode> : content);
  if (!task) fireEvent.click(screen.getByRole('button', { name: record ? 'Edit display name' : `Create ${kind.toLowerCase()}` }));
  return router;
}
function change(label: string, value: string) { setField(screen.getByLabelText(label), value); }
function fill(kind: ManagedKind) {
  change('Record name', 'synthetic-record'); change('Display name', 'Synthetic record');
  if (kind === 'Rack') change('Facility', facilities[0].metadata.name);
  if (kind === 'Device') { change('Installation plan', 'plan-uid'); change('Model', 'Synthetic model'); change('Serial number', 'SYNTHETIC'); }
  if (kind === 'DeviceClaim') { change('Expected model', 'Synthetic model'); change('Location', 'facility-uid'); }
  if (kind === 'Cable') {
    change('Cable medium', 'Power');
    chooseEndpoint('Lane 1 end A', 'Feed A'); chooseEndpoint('Lane 1 end B', 'Power supply 1');
  }
}

it.each(['Facility', 'Rack', 'Device', 'Cable', 'DeviceClaim'] as const)('validates and acknowledges %s creation in its non-expanding surface', async kind => {
  create.mockResolvedValue({} as never);
  setup(kind);
  expect(document.querySelector('.inventory-management details')).toBeNull();
  expect(screen.queryByRole('dialog') !== null).toBe(kind === 'Facility' || kind === 'Rack');
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  expect(create).not.toHaveBeenCalled();
  expect(screen.getByLabelText('Record name')).toHaveAttribute('aria-invalid', 'true');
  expect(screen.getByRole('alert')).toHaveFocus();
  fireEvent.click(within(screen.getByRole('alert')).getByRole('button', { name: /Record name/ }));
  expect(screen.getByLabelText('Record name')).toHaveFocus();
  fill(kind);
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  await screen.findByText(`${kind === 'DeviceClaim' ? 'Installation plan' : kind} created.`);
  expect(create).toHaveBeenCalledTimes(1);
  expect(create.mock.calls[0][1]).toMatchObject({ namespace: 'default', name: 'synthetic-record' });
  expect(screen.queryByLabelText('Display name')).not.toBeInTheDocument();
});

it.each(['Device', 'Cable', 'DeviceClaim'] as const)('starts %s task authority after StrictMode effect replay', async kind => {
  create.mockResolvedValue({} as never);
  setup(kind, undefined, true);
  fill(kind);
  expect(screen.getByRole('button', { name: 'Create record' })).toBeEnabled();
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  await screen.findByText(`${kind === 'DeviceClaim' ? 'Installation plan' : kind} created.`);
  expect(create).toHaveBeenCalledTimes(1);
});

it.each(['Facility', 'Rack', 'Device', 'Cable'] as const)('edits only the display name of %s using its captured baseline', async kind => {
  const metadata = { ...facilities[0].metadata, uid: `${kind}-uid`, resourceVersion: '19', managedFields: [{ manager: management.INVENTORY_FIELD_MANAGER }] };
  const record = kind === 'Device' ? { ...device, metadata } : { ...facilities[0], kind, metadata };
  edit.mockResolvedValue({} as never);
  setup(kind, record);
  expect(screen.getByRole('dialog', { name: 'Edit display name' })).toBeInTheDocument();
  expect(screen.queryByLabelText('Record name')).not.toBeInTheDocument();
  change('Display name', 'New label');
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  await screen.findByText('Display name saved.');
  expect(edit.mock.calls[0][0]).toEqual(record);
  expect(edit.mock.calls[0][0]).not.toBe(record);
  expect(create).not.toHaveBeenCalled();
});

it('traps dialog focus, confirms dirty Escape/Cancel, and returns focus to the opener', async () => {
  setup();
  // Display name is the first field; the record name follows it.
  const name = screen.getByLabelText('Display name');
  await waitFor(() => expect(name).toHaveFocus());
  fireEvent.keyDown(name, { key: 'Tab', shiftKey: true });
  expect(screen.getByRole('button', { name: 'Create record' })).toHaveFocus();
  fireEvent.keyDown(document.activeElement!, { key: 'Tab' });
  expect(name).toHaveFocus();
  change('Display name', 'Private draft');
  fireEvent.keyDown(name, { key: 'Escape' });
  expect(window.confirm).toHaveBeenCalledWith('Discard unsaved changes?');
  expect(screen.getByRole('dialog')).toBeInTheDocument();
  vi.mocked(window.confirm).mockReturnValue(true);
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  await waitFor(() => expect(screen.getByRole('button', { name: 'Create facility' })).toHaveFocus());
  fireEvent.click(screen.getByRole('button', { name: 'Create facility' }));
  expect(screen.getByLabelText('Display name')).toHaveValue('');
});

it('keeps pending and unknown writes open and never labels leaving as cancellation', async () => {
  let reject!: (reason: Error) => void;
  create.mockImplementation(() => new Promise((_resolve, fail) => { reject = fail; }));
  const router = setup(); fill('Facility');
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled();
  fireEvent.keyDown(screen.getByLabelText('Record name'), { key: 'Escape' });
  expect(screen.getByRole('dialog')).toBeInTheDocument();
  await act(async () => { await router.navigate('/elsewhere'); });
  expect(router.state.location.pathname).toBe('/');
  expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('Leaving does not cancel the write'));
  await act(async () => reject(new ApiError(503, 'private upstream details')));
  expect(screen.getByRole('status')).toHaveTextContent('outcome is unknown');
  expect(screen.getByRole('button', { name: 'Create record' })).toBeDisabled();
  expect(screen.queryByRole('button', { name: 'Cancel' })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Leave form' }));
  expect(screen.getByRole('dialog')).toBeInTheDocument();
  expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('does not cancel or undo'));
  expect(create).toHaveBeenCalledTimes(1);
});

it('protects dirty task navigation and unload without persisting the draft', async () => {
  const router = setup('Device'); change('Display name', 'Private draft');
  const unload = new Event('beforeunload', { cancelable: true });
  window.dispatchEvent(unload);
  expect(unload.defaultPrevented).toBe(true);
  await act(async () => { await router.navigate('/elsewhere'); });
  expect(screen.getByLabelText('Display name')).toHaveValue('Private draft');
  vi.mocked(window.confirm).mockReturnValue(true);
  await act(async () => { await router.navigate('/elsewhere'); });
  await screen.findByText('Other page');
  await act(async () => { await router.navigate(-1); });
  expect(screen.getByLabelText('Display name')).toHaveValue('');
});

it('does not echo reference choices into validation summaries or steal focus while correcting fields', () => {
  setup('Device');
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  expect(screen.getByRole('alert')).not.toHaveTextContent(claim.spec.displayName!);
  const name = screen.getByLabelText('Record name');
  name.focus();
  fireEvent.input(name, { target: { value: 'synthetic' } });
  expect(name).toHaveFocus();
  expect(name).not.toHaveAttribute('aria-invalid');
});

it('keeps reported-location correction separate and confirms dirty dismissal', async () => {
  placement.mockResolvedValue({} as never);
  render(<QueryClientProvider client={new QueryClient()}><DevicePlacementEdit device={device} locations={[]} /></QueryClientProvider>);
  fireEvent.click(screen.getByRole('button', { name: /Correct location|Add reported location/ }));
  expect(screen.getByRole('dialog', { name: 'Correct reported location' })).toBeInTheDocument();
  expect(screen.queryByLabelText('Display name')).not.toBeInTheDocument();
  change('Correction reason', 'Correct record'); change('Evidence reference', 'ticket/synthetic');
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
  expect(screen.getByRole('dialog')).toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  await screen.findByText('Location saved.');
  expect(placement.mock.calls[0][0]).toEqual(device);
  expect(edit).not.toHaveBeenCalled();
});

it('keeps post-dispatch invalidation unknown in the dialog', async () => {
  create.mockImplementation(async (_kind, _values, operation) => {
    const scope = new InventoryOperationScope();
    const dispatched = scope.begin(); dispatched.dispatch(); scope.invalidate(); dispatched.assertCurrent();
    operation.assertCurrent();
    return {} as never;
  });
  setup(); fill('Facility'); fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  await screen.findByText(/outcome is unknown/);
  expect(screen.getByRole('dialog')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Leave form' })).toBeInTheDocument();
  expect(screen.queryByText('Facility created.')).not.toBeInTheDocument();
});
