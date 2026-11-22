import { afterEach, describe, expect, it, vi } from 'vitest';
import { waitFor } from '@testing-library/react';
import { createInventoryRecord, editInventoryLabel, INVENTORY_FIELD_MANAGER, inventoryPayload, type InventoryFormValues } from './inventory-management';
import { hardwarePayload, type ReferencedEndpoint } from './inventory-hardware';
import { InventoryOperationScope } from './inventory-operation';

const values: InventoryFormValues = { name: 'synthetic', namespace: 'default', displayName: 'Synthetic hardware', recordedBy: 'operator', reference: '', height: '', facility: '', model: 'Example', serial: 'SYNTHETIC-1', claim: { name: 'plan', uid: 'plan-uid', resourceVersion: '1' }, components: [{ id: 'port', name: 'Port', type: 'NetworkPort', positions: 1 }] };
const endpoint: ReferencedEndpoint = { kind: 'Device', name: 'synthetic-device', uid: 'device-uid', resourceVersion: '1', componentID: 'port', position: 1 };
const cableValues: InventoryFormValues = { ...values, medium: 'Copper', cableLanes: [{ id: 'lane-1', a: endpoint, b: { ...endpoint, name: 'other', uid: 'other-uid' } }] };
const planValues: InventoryFormValues = { ...values, planLocation: { kind: 'Rack', name: 'rack', uid: 'rack-uid', resourceVersion: '1' } };
const common = inventoryPayload('Facility', values, '2026-01-01T00:00:00Z');
const claim = { metadata: { name: 'plan', uid: 'plan-uid', resourceVersion: '1' }, spec: { lifecycle: 'Installation' } };
const device = { ...hardwarePayload('Device', values, common), metadata: { name: 'synthetic-device', namespace: 'default', uid: 'device-uid', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } };
const response = (data: unknown, status = 200) => ({ ok: status < 400, status, json: async () => ({ data }) });
afterEach(() => vi.unstubAllGlobals());

describe('catalog selection for new devices', () => {
  const selection = { namespace: 'default', name: 'model-stable', uid: 'model-uid', resourceVersion: '3', manufacturer: { name: 'vendor-stable', uid: 'vendor-uid', resourceVersion: '4' } };
  const model = { metadata: { namespace: 'default', name: 'model-stable', uid: 'model-uid', resourceVersion: '3' }, spec: { manufacturerRef: { name: 'vendor-stable' }, componentTemplates: [{ id: 'not-installed' }], modelShape: { heightUnits: 8 } } };
  const manufacturer = { metadata: { namespace: 'default', name: 'vendor-stable', uid: 'vendor-uid', resourceVersion: '4' } };

  it('rechecks catalog identities and revisions, sending only name references under strict validation', async () => {
    const requests = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(claim)).mockResolvedValueOnce(response(model)).mockResolvedValueOnce(response(manufacturer)).mockResolvedValueOnce(response({ metadata: { uid: 'new-device' } }));
    vi.stubGlobal('fetch', requests);
    await createInventoryRecord('Device', { ...values, catalog: selection }, new InventoryOperationScope().begin());
    const payload = JSON.parse(requests.mock.calls[4][1].body);
    expect(requests.mock.calls[4][0]).toContain('fieldValidation=Strict');
    expect(payload.spec.deviceTypeRef).toEqual({ name: 'model-stable' });
    expect(payload.spec.identity.model).toBe(values.model);
    expect(payload.spec.components).toEqual(values.components);
    expect(payload.spec.modelShape).toBeUndefined();
    expect(payload.spec.reportedPlacement).toBeUndefined();
    expect(JSON.stringify(payload.spec)).not.toContain('uid');
  });

  it.each(['uid', 'resourceVersion', 'namespace', 'name', 'deletionTimestamp'])('rejects changed catalog %s before registration', async field => {
    const changed = { ...model, metadata: { ...model.metadata, [field]: 'changed' } };
    const requests = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(claim)).mockResolvedValueOnce(response(changed));
    vi.stubGlobal('fetch', requests);
    await expect(createInventoryRecord('Device', { ...values, catalog: selection }, new InventoryOperationScope().begin())).rejects.toMatchObject({ status: 409 });
    expect(requests).toHaveBeenCalledTimes(3);
  });

  it.each([401, 403, 404, 503])('does not register after catalog denial/unavailability %s', async status => {
    for (const target of ['model', 'manufacturer']) {
      const requests = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(claim));
      if (target === 'manufacturer') requests.mockResolvedValueOnce(response(model));
      requests.mockResolvedValueOnce(response(undefined, status));
      vi.stubGlobal('fetch', requests);
      await expect(createInventoryRecord('Device', { ...values, catalog: selection }, new InventoryOperationScope().begin())).rejects.toMatchObject({ status });
      expect(requests).toHaveBeenCalledTimes(target === 'model' ? 3 : 4);
    }
  });

  it('fences a late catalog read after session revocation', async () => {
    const scope = new InventoryOperationScope();
    let release: ((value: unknown) => void) | undefined;
    const requests = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(claim)).mockImplementationOnce(() => new Promise(resolve => { release = resolve; }));
    vi.stubGlobal('fetch', requests);
    const outcome = createInventoryRecord('Device', { ...values, catalog: selection }, scope.begin()).catch(error => error);
    await waitFor(() => expect(release).toBeDefined());
    scope.invalidate();
    release!(response(model));
    expect(await outcome).toMatchObject({ dispatched: false });
    expect(requests).toHaveBeenCalledTimes(3);
  });
});

describe('physical record creation', () => {
  it.each(['Facility', 'Rack'] as const)('creates a new observational plan at a %s without inventing geometry', async kind => {
    const planValues = { ...values, planLocation: { kind, name: 'location', uid: 'location-uid', resourceVersion: '1' } };
    const requests = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response({ metadata: { uid: 'location-uid', resourceVersion: '1' } })).mockResolvedValueOnce(response({ metadata: { uid: 'plan-uid' } }));
    vi.stubGlobal('fetch', requests);
    await createInventoryRecord('DeviceClaim', planValues, new InventoryOperationScope().begin());
    const payload = JSON.parse(requests.mock.calls[2][1].body);
    expect(payload.spec.lifecycle).toBe('Installation');
    expect(payload.spec.placement).toEqual(kind === 'Facility' ? { scope: 'Facility', facilityRef: { name: 'location' } } : { scope: 'RackMembership', rackRef: { name: 'location' } });
    expect(payload.status).toBeUndefined();
  });
  it('creates typed immutable identity without assigning IP, status, or capacity', async () => {
    const requests = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(claim)).mockResolvedValueOnce(response(device));
    vi.stubGlobal('fetch', requests);
    await createInventoryRecord('Device', values, new InventoryOperationScope().begin());
    const body = JSON.parse(requests.mock.calls[2][1].body);
    expect(body.kind).toBe('Device');
    expect(body.spec.claimRef).toEqual({ name: 'plan' });
    expect(body.spec.identity).toEqual({ model: 'Example', serial: 'SYNTHETIC-1' });
    expect(body.spec.components[0].type).toBe('NetworkPort');
    expect(Object.keys(body.spec).sort()).toEqual(['claimRef', 'components', 'displayName', 'identity', 'provenance', 'topologyAuthority'].sort());
    expect(body.status).toBeUndefined();
  });
  it.each([{ serial: '', systemUUID: '' }, { model: '' }, { systemUUID: 'invented' }, { components: [{ id: 'a', name: 'A', type: 'NetworkPort', positions: 0 }] }, { components: [values.components![0], values.components![0]] }])('rejects incomplete or invalid hardware: %j', invalid => {
    expect(() => hardwarePayload('Device', { ...values, ...invalid } as InventoryFormValues, common)).toThrow();
  });
  it.each(['legacy', 'changed', 'deleted'])('does not register against %s plans', async mode => {
    const changed = { ...claim, metadata: { ...claim.metadata, ...(mode === 'changed' && { resourceVersion: '2' }), ...(mode === 'deleted' && { deletionTimestamp: 'now' }) }, spec: { lifecycle: mode === 'legacy' ? undefined : 'Installation' } };
    const requests = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(changed));
    vi.stubGlobal('fetch', requests);
    await expect(createInventoryRecord('Device', values, new InventoryOperationScope().begin())).rejects.toMatchObject({ status: 409 });
    expect(requests).toHaveBeenCalledTimes(2);
  });
  it('creates typed cable lanes only after re-reading endpoint identities and positions', async () => {
    const requests = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(device)).mockResolvedValueOnce(response({ ...device, metadata: { ...device.metadata, uid: 'other-uid' } })).mockResolvedValueOnce(response({ metadata: { uid: 'cable-uid' } }));
    vi.stubGlobal('fetch', requests);
    await createInventoryRecord('Cable', cableValues, new InventoryOperationScope().begin());
    const payload = JSON.parse(requests.mock.calls[3][1].body);
    expect(payload.spec.lanes).toEqual([{ id: 'lane-1', a: { deviceRef: { name: 'synthetic-device' }, componentID: 'port', position: 1 }, b: { deviceRef: { name: 'other' }, componentID: 'port', position: 1 } }]);
    expect(payload.status).toBeUndefined();
  });
  it('rejects repeated endpoints across lanes and self-connections', () => {
    for (const cableLanes of [[{ id: 'lane', a: endpoint, b: endpoint }], [...cableValues.cableLanes!, { ...cableValues.cableLanes![0], id: 'lane-2' }]]) expect(() => hardwarePayload('Cable', { ...cableValues, cableLanes }, common)).toThrow();
  });
  it.each(['revision', 'missing-component', 'unknown-positions', 'not-a-port'])('refuses a cable with %s', async mode => {
    const changed = { ...device, metadata: { ...device.metadata, resourceVersion: mode === 'revision' ? '2' : '1' }, spec: { ...device.spec, components: mode === 'missing-component' ? [] : [{ ...values.components![0], positions: mode === 'unknown-positions' ? undefined : 1, type: mode === 'not-a-port' ? 'Module' : 'NetworkPort' }] } };
    const requests = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(changed));
    vi.stubGlobal('fetch', requests);
    await expect(createInventoryRecord('Cable', cableValues, new InventoryOperationScope().begin())).rejects.toMatchObject({ status: 409 });
    expect(requests).toHaveBeenCalledTimes(2);
  });
  it.each(['Device', 'Cable', 'DeviceClaim'] as const)('keeps %s submit authority revoked after a delayed prerequisite', async kind => {
    const scope = new InventoryOperationScope();
    let release: ((value: unknown) => void) | undefined;
    const requests = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockImplementationOnce(() => new Promise(resolve => { release = resolve; }));
    vi.stubGlobal('fetch', requests);
    const outcome = createInventoryRecord(kind, kind === 'Device' ? values : kind === 'Cable' ? cableValues : planValues, scope.begin()).catch(error => error);
    await waitFor(() => expect(release).toBeDefined());
    scope.invalidate();
    release!(response(kind === 'Device' ? claim : device));
    expect(await outcome).toMatchObject({ dispatched: false });
    expect(requests).toHaveBeenCalledTimes(2);
  });
  it.each(['Device', 'Cable', 'DeviceClaim'] as const)('does not dispatch %s after denied/expired prerequisite', async kind => {
    for (const status of [401, 403, 404, 503]) {
      const requests = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(undefined, status));
      vi.stubGlobal('fetch', requests);
      await expect(createInventoryRecord(kind, kind === 'Device' ? values : kind === 'Cable' ? cableValues : planValues, new InventoryOperationScope().begin())).rejects.toMatchObject({ status });
      expect(requests).toHaveBeenCalledTimes(2);
    }
  });
  it('limits device edits to labels/provenance, preserving identity and components', async () => {
    const requests = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(device)).mockResolvedValueOnce(response(device));
    vi.stubGlobal('fetch', requests);
    await editInventoryLabel(device, values, new InventoryOperationScope().begin());
    expect(JSON.parse(requests.mock.calls[2][1].body).map((operation: { path: string }) => operation.path)).toEqual(['/metadata/uid', '/metadata/resourceVersion', '/spec/displayName', '/spec/provenance']);
  });
});
