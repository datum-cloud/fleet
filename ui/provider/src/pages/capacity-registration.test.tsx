import { chooseValue, selectOptions } from '../test/field-select';
import { beforeEach, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { CreateCapacityDialog } from '../components/create-capacity-dialog';
import type { Device } from '../../../shared/fleet-api';

const device: Device = { metadata: { name: 'server-a', namespace: 'default', uid: 'device-1', resourceVersion: '1' }, spec: { claimRef: { name: 'plan' }, identity: { model: 'test', serial: 'serial', systemUUID: '11111111-1111-1111-1111-111111111111' } } };
const fetcher = vi.fn<typeof fetch>();
beforeEach(() => {
  fetcher.mockReset(); vi.stubGlobal('fetch', fetcher);
  fetcher.mockImplementation(async (input, init) => {
    if (String(input).includes('selfsubjectaccessreviews')) return Response.json({ data: { status: { allowed: true } } });
    if (init?.method === 'POST') { const body = JSON.parse(String(init.body)); return Response.json({ data: { ...body, metadata: { ...body.metadata, uid: 'capacity-1' } } }); }
    return Response.json({ data: String(input).endsWith('/devices') ? { items: [device] } : device });
  });
});

async function openForm(onRegistered = vi.fn()) {
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'operator-1' } } }), element: <Outlet />, children: [{ index: true, element: <CreateCapacityDialog onRegistered={onRegistered} /> }] }]);
  render(<RouterProvider router={router} />);
  const trigger = await screen.findByRole('button', { name: 'Register capacity' });
  await waitFor(() => expect(trigger).toBeEnabled()); fireEvent.click(trigger);
  // The Select's hidden form select gains an option once the Devices load; opening the Select inside waitFor would loop on its own DOM changes.
  await waitFor(() => expect(document.querySelector('select[aria-hidden="true"] option[value="device-1"]')).not.toBeNull());
  expect(selectOptions(screen.getByLabelText('Device')).map(option => option.text)).toContain('server-a');
  chooseValue(screen.getByLabelText('Device'), 'device-1');
  return router;
}

it('registers the selected incarnation through a real host session fence', async () => {
  const onRegistered = vi.fn();
  await openForm(onRegistered);
  expect(screen.getByRole('dialog')).toHaveTextContent('Creates a Disabled/Retain MachineCapacity from an Active Device in default.');
  expect(screen.getByRole('dialog')).not.toHaveTextContent('Physical inventory records only');
  fireEvent.click(screen.getByRole('button', { name: 'Register Disabled capacity' }));
  await waitFor(() => expect(onRegistered).toHaveBeenCalledTimes(1));
  expect(fetcher.mock.calls.filter(([url, init]) => String(url).includes('machinecapacities?') && init?.method === 'POST')).toHaveLength(1);
  expect(screen.queryByRole('checkbox')).toBeNull();
});

it('leaves no message beside the trigger after a confirmed registration', async () => {
  await openForm();
  fireEvent.click(screen.getByRole('button', { name: 'Register Disabled capacity' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(screen.queryByRole('alert')).toBeNull();
  expect(screen.queryByText(/Registered device-device-1/)).toBeNull();
});

it('gives a denied trigger its reason as a tooltip, not page text', async () => {
  fetcher.mockImplementation(async () => Response.json({ data: { status: { allowed: false } } }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'operator-1' } } }), element: <Outlet />, children: [{ index: true, element: <CreateCapacityDialog /> }] }]);
  render(<RouterProvider router={router} />);
  const trigger = await screen.findByRole('button', { name: 'Register capacity' });
  await waitFor(() => expect(trigger).toHaveAccessibleDescription('Registration denied or unavailable for this session.'));
  expect(trigger).toBeDisabled();
  expect(trigger.parentElement).toHaveAttribute('title', 'Registration denied or unavailable for this session.');
  expect(screen.queryByRole('alert')).toBeNull();
});

it('visible blur/focus is a no-op for the selected baseline and draft', async () => {
  await openForm();
  const before = fetcher.mock.calls.length;
  fireEvent(window, new Event('blur')); fireEvent(window, new Event('focus'));
  expect(screen.getByText('device-device-1')).toBeInTheDocument();
  expect(screen.getByLabelText('Device')).toHaveTextContent('server-a');
  expect(fetcher.mock.calls).toHaveLength(before);
});

it('keeps unknown completion in the form and prevents retry and rollback', async () => {
  await openForm();
  fetcher.mockImplementation(async (input, init) => {
    if (String(input).includes('selfsubjectaccessreviews')) return Response.json({ data: { status: { allowed: true } } });
    if (init?.method === 'POST') throw new Error('network lost');
    return Response.json({ data: device });
  });
  fireEvent.click(screen.getByRole('button', { name: 'Register Disabled capacity' }));
  expect(await screen.findByText(/Check whether capacity device-device-1 exists/)).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Register Disabled capacity' })).toBeDisabled();
  expect(screen.getByLabelText('Device')).toHaveTextContent('server-a');
  expect(fetcher.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false);
});

it('lists ineligible Devices, including one without a uid, as disabled choices with their reason', async () => {
  const unidentified: Device = { metadata: { name: 'server-b', namespace: 'default' }, spec: { ...device.spec, identity: { model: 'test', serial: 'serial-b', systemUUID: '22222222-2222-2222-2222-222222222222' } } };
  fetcher.mockImplementation(async input => {
    if (String(input).includes('selfsubjectaccessreviews')) return Response.json({ data: { status: { allowed: true } } });
    return Response.json({ data: { items: [device, unidentified] } });
  });
  render(<RouterProvider router={createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'operator-1' } } }), element: <Outlet />, children: [{ index: true, element: <CreateCapacityDialog /> }] }])} />);
  const trigger = await screen.findByRole('button', { name: 'Register capacity' });
  await waitFor(() => expect(trigger).toBeEnabled()); fireEvent.click(trigger);
  await waitFor(() => expect(document.querySelector('select[aria-hidden="true"] option[value="device-1"]')).not.toBeNull());
  const options = selectOptions(screen.getByLabelText('Device'));
  expect(options.map(option => [option.text, option.disabled])).toEqual([['server-a', false], ['server-b — Device incarnation is unavailable or deleting', true]]);
  expect(screen.getByLabelText('Device')).toHaveTextContent('Choose an existing Device');
});
