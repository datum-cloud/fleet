import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { PhysicalInventory } from './physical-inventory';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';
import { toInventory, type Inventory } from '../inventory';
import { PLUGIN_ID, PROVIDER_PROJECT } from '../lib/api';
import { readInventoryReference, InventoryReferenceReadError } from '../lib/inventory-read-access';
import { InventoryOperationScope } from '../lib/inventory-operation';

const response = (data: unknown, status = 200) => ({ ok: status < 400, status, json: async () => ({ data }) });
afterEach(() => vi.unstubAllGlobals());

it.each([false, true])('removes denied prerequisite choices and cached records, integrated=%s', async integrated => {
  const claim = structuredClone(fixturePhysicalClaims[0]);
  claim.metadata = { ...claim.metadata, uid: 'plan-uid', resourceVersion: '1' };
  claim.spec = { ...claim.spec, displayName: 'Restricted plan label', lifecycle: 'Installation' };
  const inventory = toInventory([claim], fixturePhysicalDevices, fixturePhysicalRecords);
  const client = new QueryClient();
  const queryKey = [PLUGIN_ID, 'inventory', PROVIDER_PROJECT, 'test'];
  client.setQueryData(queryKey, inventory);
  const writes: unknown[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    if (init.method === 'GET') return response(undefined, 403);
    writes.push(init); return response({});
  }));
  render(<QueryClientProvider client={client}><MemoryRouter initialEntries={['/?kind=Device']}>
    {integrated ? <PhysicalInventory inventory={inventory} /> : <InventoryManagement kind="Device" compact facilities={[]} claims={[claim]} />}
  </MemoryRouter></QueryClientProvider>);
  fireEvent.click(await screen.findByRole('button', { name: 'Create device' }));
  for (const [label, value] of [['Record name', 'synthetic-new'], ['Display name', 'Safe unsaved name'], ['Model', 'Example'], ['Serial number', 'SYNTHETIC-NEW'], ['Installation plan', 'plan-uid']]) fireEvent.change(screen.getByLabelText(label), { target: { value } });
  fireEvent.click(screen.getByRole('button', { name: 'Create device record' }));
  await screen.findByText(/Restricted choices were removed/);
  expect(writes).toEqual([]);
  expect(screen.queryByText('Restricted plan label')).not.toBeInTheDocument();
  expect(screen.getByLabelText('Installation plan')).toHaveValue('');
  expect(screen.getByLabelText('Display name')).toHaveValue('Safe unsaved name');
  expect(screen.getByRole('button', { name: 'Create device record' })).toBeDisabled();
  await waitFor(() => expect(client.getQueryData<Inventory>(queryKey)?.slots).toEqual([]));
  expect(client.getQueryData<Inventory>(queryKey)?.issues?.DeviceClaim?.status).toBe(403);
});

it.each(['Facility', 'Rack'] as const)('redacts denied %s references in PowerFeed rows and search', kind => {
  const feed = structuredClone(fixturePhysicalRecords.find(record => record.kind === 'PowerFeed')!);
  delete feed.spec.rackRef;
  delete feed.spec.facilityRef;
  feed.spec[kind === 'Facility' ? 'facilityRef' : 'rackRef'] = { name: 'restricted-location' };
  const inventory = { ...toInventory([], [], [feed]), issues: { [kind]: { status: 403 } } };
  render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={['/?kind=PowerFeed']}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
  expect(screen.queryByText('restricted-location')).not.toBeInTheDocument();
  fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'restricted-location' } });
  expect(screen.queryByRole('link', { name: feed.spec.displayName })).not.toBeInTheDocument();
});

it.each(['Device', 'PowerFeed'] as const)('does not search denied %s cable endpoints', kind => {
  const cable = structuredClone(fixturePhysicalRecords.find(record => record.kind === 'Cable')!);
  cable.spec.lanes![0].a = { componentID: 'port', position: 1, ...(kind === 'Device' ? { deviceRef: { name: 'restricted-endpoint' } } : { powerFeedRef: { name: 'restricted-endpoint' } }) };
  const inventory = { ...toInventory([], [], [cable]), issues: { [kind]: { status: 403 } } };
  render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={['/?kind=Cable']}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
  fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'restricted-endpoint' } });
  expect(screen.queryByRole('link', { name: cable.spec.displayName })).not.toBeInTheDocument();
  fireEvent.change(screen.getByRole('searchbox'), { target: { value: cable.spec.displayName! } });
  expect(screen.getByRole('link', { name: cable.spec.displayName })).toBeInTheDocument();
});

it.each(['Facility', 'Rack', 'DeviceClaim', 'Device', 'PowerFeed'] as const)('retains the failed %s read scope separately from write denial', async kind => {
  vi.stubGlobal('fetch', vi.fn(async () => response(undefined, 403)));
  const operation = new InventoryOperationScope().begin();
  const failure = await readInventoryReference(kind, 'synthetic-namespace', 'synthetic-record', operation).catch(error => error);
  expect(failure).toBeInstanceOf(InventoryReferenceReadError);
  expect(failure).toMatchObject({ kind, namespace: 'synthetic-namespace', status: 403 });
});
