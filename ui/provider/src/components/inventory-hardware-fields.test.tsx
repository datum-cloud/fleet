import { chooseValue, selectOptions } from '../test/field-select';
import { chooseEndpoint } from '../test/cable-endpoint';
import { findEnabledButton } from '../test/enabled-button';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';
import { buttonVariants } from '@datum-cloud/datum-ui/button';
import { cn } from '@datum-cloud/datum-ui/utils';

const claim = { ...fixturePhysicalClaims[0], metadata: { ...fixturePhysicalClaims[0].metadata, uid: 'plan-uid', resourceVersion: '1' }, spec: { ...fixturePhysicalClaims[0].spec, lifecycle: 'Installation' as const } };
const device = { ...fixturePhysicalDevices[0], metadata: { ...fixturePhysicalDevices[0].metadata, uid: 'device-uid', resourceVersion: '1' } };
const feed = { ...fixturePhysicalRecords[2], metadata: { ...fixturePhysicalRecords[2].metadata, uid: 'feed-uid', resourceVersion: '1' } };
const response = (data: unknown, status = 200) => ({ ok: status < 400, status, json: async () => ({ data }) });
afterEach(() => vi.unstubAllGlobals());
function renderForm(kind: 'Device' | 'Cable') {
  return render(<QueryClientProvider client={new QueryClient()}><InventoryManagement kind={kind} facilities={[]} claims={[claim]} devices={[device]} feeds={[feed]} /></QueryClientProvider>);
}
async function open(kind: 'Device' | 'Cable') {
  fireEvent.click(await findEnabledButton({ name: `Create ${kind.toLowerCase()}` }));
  for (const [label, value] of [['Record name', 'synthetic-new'], ['Display name', 'Synthetic record']]) fireEvent.change(screen.getByLabelText(label), { target: { value } });
}
it('registers hardware through the real form without changing claim identity', async () => {
  const writes: unknown[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    if (init.method === 'GET') return response(claim);
    writes.push(JSON.parse(init.body as string)); return response({ metadata: { uid: 'new-uid' } });
  }));
  renderForm('Device'); await open('Device');
  const model = screen.getByLabelText('Model');
  expect(model).toHaveAttribute('data-slot', 'input');
  expect(model.closest('label')).toHaveAttribute('data-slot', 'label');
  expect(model).toBeRequired();
  expect(screen.queryByLabelText('Management namespace')).not.toBeInTheDocument();
  const addComponent = screen.getByRole('button', { name: 'Add component' });
  expect(addComponent).toHaveClass(...cn(buttonVariants({ type: 'secondary', theme: 'outline', size: 'small' })).split(' '));
  expect(addComponent).toHaveAttribute('type', 'button');
  chooseValue(screen.getByLabelText('Installation plan'), 'plan-uid');
  fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'Recorded model' } });
  fireEvent.change(screen.getByLabelText('Serial number'), { target: { value: 'SYNTHETIC-NEW' } });
  fireEvent.click(screen.getByRole('button', { name: 'Add component' }));
  fireEvent.change(screen.getByLabelText('Component 1 id'), { target: { value: 'nic-1' } });
  fireEvent.change(screen.getByLabelText('Component 1 name'), { target: { value: 'NIC port 1' } });
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  await screen.findByText('Device created.');
  expect(writes).toHaveLength(1);
  expect(writes[0]).toMatchObject({ kind: 'Device', spec: { claimRef: { name: claim.metadata.name }, identity: { serial: 'SYNTHETIC-NEW' }, components: [{ id: 'nic-1', type: 'NetworkPort' }] } });
});
it('uses typed feed and device endpoints and retains input on submit denial', async () => {
  let reviews = 0;
  const writes: unknown[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: ++reviews === 1 } });
    writes.push(init); return response({});
  }));
  renderForm('Cable'); await open('Cable');
  chooseValue(screen.getByLabelText('Cable medium'), 'Power');
  chooseEndpoint('Lane 1 end A', 'Feed A'); chooseEndpoint('Lane 1 end B', 'Power supply 1');
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  await screen.findByText(/Your management permission changed/);
  expect(screen.getByLabelText('Display name')).toHaveValue('Synthetic record');
  expect(screen.getByRole('button', { name: 'Create record' })).toBeDisabled();
  expect(writes).toEqual([]);
});
it('does not offer unknown or inaccessible endpoint positions', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ status: { allowed: true } })));
  render(<QueryClientProvider client={new QueryClient()}><InventoryManagement kind="Cable" facilities={[]} devices={[{ ...device, spec: { ...device.spec, components: [{ id: 'unknown', name: 'Unknown positions', type: 'NetworkPort' }] } }]} /></QueryClientProvider>);
  await open('Cable');
  expect(selectOptions(screen.getByLabelText('Lane 1 end A device')).map(option => option.value)).toEqual(['']);
});
it('does not present an unreadable installation collection as an empty device prerequisite', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ status: { allowed: true } })));
  render(<QueryClientProvider client={new QueryClient()}><InventoryManagement kind="Device" facilities={[]} issues={{ DeviceClaim: { status: 403 } }} /></QueryClientProvider>);
  await waitFor(() => expect(screen.getByRole('button', { name: 'Create device' })).toHaveAccessibleDescription('References unreadable'));
  expect(screen.getByRole('button', { name: 'Create device' })).toBeDisabled();
  expect(screen.getByRole('status')).toBeEmptyDOMElement();
});
it('keeps readable cable endpoint choices with a partial-access explanation', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ status: { allowed: true } })));
  render(<QueryClientProvider client={new QueryClient()}><InventoryManagement kind="Cable" facilities={[]} devices={[device]} issues={{ PowerFeed: { status: 403 } }} /></QueryClientProvider>);
  await open('Cable');
  expect(screen.getByText('Some devices or feeds unreadable — list incomplete')).toBeInTheDocument();
  const sources = selectOptions(screen.getByLabelText('Lane 1 end A device'));
  expect(sources.length).toBeGreaterThan(1);
  expect(sources.map(option => option.text).join('\n')).not.toMatch(/Feed A/);
});
it('revokes a pending cable form on unmount before delayed endpoint validation completes', async () => {
  let release: ((data: unknown) => void) | undefined;
  const writes: unknown[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    if (init.method === 'GET') return new Promise(resolve => { release = resolve; });
    writes.push(init); return response({});
  }));
  const mounted = renderForm('Cable'); await open('Cable');
  chooseValue(screen.getByLabelText('Cable medium'), 'Power');
  chooseEndpoint('Lane 1 end A', 'Feed A'); chooseEndpoint('Lane 1 end B', 'Power supply 1');
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  await waitFor(() => expect(release).toBeDefined()); mounted.unmount();
  await act(async () => { release!(response(feed)); });
  expect(writes).toEqual([]);
});
