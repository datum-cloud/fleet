import { afterEach, expect, it, vi } from 'vitest';
import { inventoryResources, type InventoryKind } from '../inventory';
import { fetchInventoryCollection } from './api';
import { readInventoryReference } from './inventory-read-access';
import { createInventoryRecord, editInventoryLabel, inventoryPermission, INVENTORY_FIELD_MANAGER, type ManagedKind } from './inventory-management';
import { canCorrectPlacement, correctDevicePlacement, placementFields } from './device-placement-edit';
import { InventoryOperationScope } from './inventory-operation';
import { fixturePhysicalDevices } from './fixtures';

const kinds = Object.keys(inventoryResources) as InventoryKind[];
const managed: ManagedKind[] = ['Facility', 'Rack', 'Device', 'DeviceClaim', 'Cable'];
const values = { name: 'synthetic', namespace: 'default', displayName: 'Synthetic', facility: 'facility', height: '' };
const response = (data: unknown) => Response.json({ data });
afterEach(() => vi.unstubAllGlobals());

it.each(kinds)('scopes every %s collection page to default and rejects foreign or missing namespaces', async kind => {
  const local = { kind, metadata: { name: 'same-name', namespace: 'default' } };
  const foreign = { kind, metadata: { name: 'same-name', namespace: 'other' } };
  const fetch = vi.fn().mockResolvedValueOnce(response({ items: [local, foreign], metadata: { continue: 'next/+/=' } })).mockResolvedValueOnce(response({ items: [foreign, { kind, metadata: { name: 'missing' } }] }));
  vi.stubGlobal('fetch', fetch);
  expect(await fetchInventoryCollection(inventoryResources[kind])).toEqual([local]);
  expect(fetch).toHaveBeenCalledTimes(2);
  for (const [url, init] of fetch.mock.calls) {
    expect(url).toContain(`/projects/datum-cloud/control-plane/apis/fleet.datumapis.com/v1alpha1/namespaces/default/${inventoryResources[kind]}?`);
    expect(init.method).toBe('GET');
  }
  expect(fetch.mock.calls[1][0]).toContain('continue=next%2F%2B%2F%3D');
});

it.each(kinds)('refuses nondefault %s references before fetching and checks returned scope', async kind => {
  const fetch = vi.fn().mockResolvedValue(response({ kind, metadata: { name: 'record', namespace: 'other' } }));
  vi.stubGlobal('fetch', fetch);
  for (const namespace of ['other', '', 'default/../other']) {
    await expect(readInventoryReference(kind, namespace, 'record', new InventoryOperationScope().begin())).rejects.toMatchObject({ status: 422 });
  }
  expect(fetch).not.toHaveBeenCalled();
  await expect(readInventoryReference(kind, 'default', 'record', new InventoryOperationScope().begin())).rejects.toMatchObject({ kind, namespace: 'default', status: 409 });
  expect(fetch.mock.calls[0][0]).toContain(`/namespaces/default/${inventoryResources[kind]}/record`);
});

it.each(managed)('rejects foreign %s permission, creation and record edits without any request', async kind => {
  const fetch = vi.fn();
  vi.stubGlobal('fetch', fetch);
  expect(await inventoryPermission(kind, 'other', 'create')).toBe(false);
  expect(await inventoryPermission(kind, 'other', 'patch', 'synthetic')).toBe(false);
  await expect(createInventoryRecord(kind, { ...values, namespace: 'other' }, new InventoryOperationScope().begin())).rejects.toMatchObject({ status: 422 });
  const record = { kind, metadata: { name: 'synthetic', namespace: 'other', uid: 'uid', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] }, spec: { displayName: 'Foreign' } };
  await expect(editInventoryLabel(record as Parameters<typeof editInventoryLabel>[0], values, new InventoryOperationScope().begin())).rejects.toMatchObject({ status: 409 });
  expect(fetch).not.toHaveBeenCalled();
});

it.each(managed)('scopes %s management reviews to default with exact verb and identity', async kind => {
  const fetch = vi.fn().mockImplementation(() => response({ status: { allowed: true } }));
  vi.stubGlobal('fetch', fetch);
  expect(await inventoryPermission(kind, 'default', 'patch', 'synthetic')).toBe(true);
  expect(JSON.parse(fetch.mock.calls[0][1].body).spec.resourceAttributes).toMatchObject({ namespace: 'default', resource: inventoryResources[kind], verb: 'patch', name: 'synthetic' });
});

it('rejects foreign location baselines without a permission review or mutation', async () => {
  const fetch = vi.fn();
  vi.stubGlobal('fetch', fetch);
  const device = { ...fixturePhysicalDevices[0], metadata: { name: 'synthetic', namespace: 'other', uid: 'uid', resourceVersion: '1' } };
  expect(canCorrectPlacement(device)).toBe(false);
  await expect(correctDevicePlacement(device, placementFields(), new InventoryOperationScope().begin())).rejects.toMatchObject({ status: 409 });
  expect(fetch).not.toHaveBeenCalled();
});

it('does not accept a path or query in place of a collection', async () => {
  const fetch = vi.fn();
  vi.stubGlobal('fetch', fetch);
  await expect(fetchInventoryCollection('../other/devices')).rejects.toMatchObject({ status: 422 });
  expect(fetch).not.toHaveBeenCalled();
});
