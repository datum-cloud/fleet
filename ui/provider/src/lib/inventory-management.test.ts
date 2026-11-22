import { InventoryOperationScope } from './inventory-operation';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { canEditInventoryLabel, createInventoryRecord, editInventoryLabel, inventoryPayload, inventoryPermission, INVENTORY_FIELD_MANAGER, type InventoryFormValues } from './inventory-management';
import type { PhysicalRecord } from '../../../shared/fleet-api';

const values: InventoryFormValues = { name: 'example-facility', namespace: 'default', displayName: 'Example facility', facility: '', height: '' };
const owned: PhysicalRecord = { ...inventoryPayload('Facility', values), metadata: { name: values.name, namespace: values.namespace, uid: 'synthetic-uid', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } };
const response = (data: unknown, status = 200) => ({ ok: status >= 200 && status < 300, status, json: async () => ({ data }) });
afterEach(() => vi.unstubAllGlobals());

describe('inventory management authority', () => {
  it.each([{ allowed: false }, { allowed: true, denied: true }, { allowed: true, evaluationError: 'Unavailable' }, {}])('fails closed for incomplete or denied review %j', async status => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ status })));
    expect(await inventoryPermission('Facility', 'default', 'create')).toBe(false);
  });

  it('reviews the exact provider-project, namespace, resource, verb and name', async () => {
    const fetch = vi.fn().mockResolvedValue(response({ status: { allowed: true } }));
    vi.stubGlobal('fetch', fetch);
    expect(await inventoryPermission('Rack', 'operator-space', 'patch', 'rack-01')).toBe(true);
    expect(fetch.mock.calls[0][0]).toContain('/projects/datum-cloud/control-plane/apis/authorization.k8s.io/v1/selfsubjectaccessreviews');
    expect(JSON.parse(fetch.mock.calls[0][1].body).spec.resourceAttributes).toEqual({ namespace: 'operator-space', name: 'rack-01', resource: 'racks', group: 'fleet.datumapis.com', version: 'v1alpha1', verb: 'patch' });
  });

  it('cannot write when permission is revoked at submit', async () => {
    const fetch = vi.fn().mockResolvedValue(response({ status: { allowed: false } }));
    vi.stubGlobal('fetch', fetch);
    await expect(createInventoryRecord('Facility', values, new InventoryOperationScope().begin())).rejects.toMatchObject({ status: 403 });
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it('creates only an explicitly named observational record, without apply or overwrite', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(owned, 201));
    vi.stubGlobal('fetch', fetch);
    expect(await createInventoryRecord('Facility', values, new InventoryOperationScope().begin())).toEqual(owned);
    const [url, init] = fetch.mock.calls[1];
    expect(url).toContain('/namespaces/default/facilities?fieldManager=fleet-staff-inventory-ui');
    expect(init.method).toBe('POST');
    const payload = JSON.parse(init.body);
    expect(payload.status).toBeUndefined();
    expect(Object.keys(payload.spec)).toEqual(['displayName']);
    expect(payload.metadata).toEqual({ name: values.name, namespace: 'default' });
  });

  it('keeps unknown geometry absent and rejects invalid heights', () => {
    expect(inventoryPayload('Rack', { ...values, facility: 'example-facility' }).spec.heightUnits).toBeUndefined();
    for (const height of ['0', '101', '1.5', '-2']) expect(() => inventoryPayload('Rack', { ...values, facility: 'example-facility', height })).toThrow();
  });

  it('rejects Flux-owned, deleting and unknown-writer records', () => {
    expect(canEditInventoryLabel(owned)).toBe(true);
    for (const metadata of [{ ...owned.metadata, managedFields: undefined }, { ...owned.metadata, managedFields: [{ manager: 'kustomize-controller' }] }, { ...owned.metadata, deletionTimestamp: '2026-01-01T00:00:00Z' }, { ...owned.metadata, labels: { 'kustomize.toolkit.fluxcd.io/name': 'inventory' } }]) expect(canEditInventoryLabel({ ...owned, metadata })).toBe(false);
  });

  it('edits only a display name with UID and revision preconditions', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(owned)).mockResolvedValueOnce(response(owned));
    vi.stubGlobal('fetch', fetch);
    await editInventoryLabel(owned, { ...values, displayName: 'New name' }, new InventoryOperationScope().begin());
    const init = fetch.mock.calls[2][1];
    expect(init.method).toBe('PATCH');
    expect(init.headers['Content-Type']).toBe('application/json-patch+json');
    expect(JSON.parse(init.body).map((operation: { path: string }) => operation.path)).toEqual(['/metadata/uid', '/metadata/resourceVersion', '/spec/displayName']);
  });

  it('does not patch after ownership or revision changes', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response({ ...owned, metadata: { ...owned.metadata, resourceVersion: '2' } }));
    vi.stubGlobal('fetch', fetch);
    await expect(editInventoryLabel(owned, values, new InventoryOperationScope().begin())).rejects.toMatchObject({ status: 409 });
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it('does not retry a backend denial or unknown save outcome', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(undefined, 403));
    vi.stubGlobal('fetch', fetch);
    await expect(createInventoryRecord('Facility', values, new InventoryOperationScope().begin())).rejects.toMatchObject({ status: 403 });
    expect(fetch).toHaveBeenCalledTimes(2);
    fetch.mockReset().mockResolvedValueOnce(response({ status: { allowed: true } })).mockRejectedValueOnce(new Error('Connection lost'));
    await expect(createInventoryRecord('Facility', values, new InventoryOperationScope().begin())).rejects.toThrow('Connection lost');
    expect(fetch).toHaveBeenCalledTimes(2);
  });
});
