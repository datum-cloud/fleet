import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Device } from '../../../shared/fleet-api';
import { fixturePhysicalDevices } from './fixtures';
import { canCorrectPlacement, correctDevicePlacement, placementFields, placementPayload } from './device-placement-edit';
import { InventoryOperationScope } from './inventory-operation';

const original: Device = { ...fixturePhysicalDevices[0], metadata: { name: 'example', namespace: 'default', uid: 'device-uid', resourceVersion: '7' }, spec: { ...fixturePhysicalDevices[0].spec, provenance: { method: 'NetBoxImport', recordedAt: '2026-01-01T00:00:00Z', recordedBy: 'original-importer', source: { system: 'example', type: 'dcim/devices', id: '1', observedAt: '2026-01-01T00:00:00Z', snapshotDigest: `sha256:${'a'.repeat(64)}`, mappingRevision: 'example' } } } };
const fields = { ...placementFields(), observedAt: '2026-01-02T00:00:00Z', reason: 'Correct location', reference: 'ticket/example' };
const response = (data: unknown, status = 200) => ({ ok: status < 400, status, json: async () => ({ data }) });
const saved = (request: RequestInit) => {
  const patch = JSON.parse(request.body as string);
  return response({ ...original, metadata: { ...original.metadata, resourceVersion: '8' }, spec: { ...original.spec, reportedPlacement: patch[2].value } });
};
afterEach(() => vi.unstubAllGlobals());

describe('direct placement correction', () => {
  it('corrects provenance-free records without inventing a receipt', async () => {
    const baseline = structuredClone(original);
    delete baseline.spec.provenance;
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(baseline)).mockImplementationOnce((_url, request) => response({ ...baseline, metadata: { ...baseline.metadata, resourceVersion: '8' }, spec: { ...baseline.spec, reportedPlacement: JSON.parse(request.body).at(-1).value } }));
    vi.stubGlobal('fetch', fetch);
    const result = await correctDevicePlacement(baseline, fields, new InventoryOperationScope().begin());
    const patch = JSON.parse(fetch.mock.calls[2][1].body);
    expect(patch.map((entry: { path: string }) => entry.path)).toEqual(['/metadata/uid', '/metadata/resourceVersion', '/spec/reportedPlacement']);
    expect(result.spec.provenance).toBeUndefined();
  });
  it.each([false, true])('adds/updates imported records without altering import provenance: %s', async existing => {
    const baseline = structuredClone(original);
    if (existing) baseline.spec.reportedPlacement = { revision: 1, observedAt: fields.observedAt, snapshotDigest: `sha256:${'a'.repeat(64)}` };
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(baseline)).mockImplementationOnce((_url, request) => saved(request));
    vi.stubGlobal('fetch', fetch);
    await correctDevicePlacement(baseline, fields, new InventoryOperationScope().begin());
    const patch = JSON.parse(fetch.mock.calls[2][1].body);
    expect(patch.slice(0, 2)).toEqual([{ op: 'test', path: '/metadata/uid', value: 'device-uid' }, { op: 'test', path: '/metadata/resourceVersion', value: '7' }]);
    expect(patch.map((entry: { path: string }) => entry.path)).toEqual(['/metadata/uid', '/metadata/resourceVersion', '/spec/reportedPlacement', '/spec/provenance/recordedAt']);
    expect(patch[2].value).toMatchObject({ method: 'Manual', revision: existing ? 2 : 1 });
    expect(patch[2].value.snapshotDigest).toBeUndefined();
    expect(baseline.spec.provenance).toEqual(original.spec.provenance);
  });
  it('does not infer geometry or location from a model or claim', () => {
    expect(placementPayload(original, fields)).toEqual({ revision: 1, observedAt: fields.observedAt.replace('Z', '.000Z'), method: 'Manual', reason: fields.reason, reference: fields.reference });
    expect(canCorrectPlacement(original)).toBe(true);
    expect(canCorrectPlacement({ ...original, metadata: { ...original.metadata, managedFields: [{ manager: 'kustomize-controller' }] } })).toBe(false);
  });
  it.each([{ position: '-1', rack: 'rack' }, { height: '0', rack: 'rack' }, { height: 'NaN', rack: 'rack' }, { position: '100', height: '2', rack: 'rack' }, { position: '1' }, { rack: 'rack', mount: 'side', face: 'front' }, { face: 'side', rack: 'rack' }, { reason: ' ' }, { reference: '' }, { observedAt: 'invalid' }, { fullDepth: 'unknown', rack: 'rack' }])('rejects malformed fields %j', invalid => {
    expect(() => placementPayload(original, { ...fields, ...invalid })).toThrow();
  });
  it.each(['uid', 'resourceVersion'])('rejects changed %s before mutation', async field => {
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response({ ...original, metadata: { ...original.metadata, [field]: 'changed' } }));
    vi.stubGlobal('fetch', fetch);
    await expect(correctDevicePlacement(original, fields, new InventoryOperationScope().begin())).rejects.toMatchObject({ status: 409 });
    expect(fetch).toHaveBeenCalledTimes(2);
  });
  it.each([401, 403, 404, 503])('does not mutate after denied permission request %s', async status => {
    const fetch = vi.fn().mockResolvedValue(response({}, status));
    vi.stubGlobal('fetch', fetch);
    await expect(correctDevicePlacement(original, fields, new InventoryOperationScope().begin())).rejects.toMatchObject({ status });
    expect(fetch).toHaveBeenCalledTimes(1);
  });
  it('does not mutate on a denied review', async () => {
    const fetch = vi.fn().mockResolvedValue(response({ status: { allowed: false } }));
    vi.stubGlobal('fetch', fetch);
    await expect(correctDevicePlacement(original, fields, new InventoryOperationScope().begin())).rejects.toMatchObject({ status: 403 });
    expect(fetch).toHaveBeenCalledTimes(1);
  });
  it.each([401, 403, 404, 409, 422, 503])('never retries a rejected/uncertain patch %s', async status => {
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(original)).mockResolvedValueOnce(response({}, status));
    vi.stubGlobal('fetch', fetch);
    await expect(correctDevicePlacement(original, fields, new InventoryOperationScope().begin())).rejects.toMatchObject({ status: status === 422 ? 409 : status });
    expect(fetch).toHaveBeenCalledTimes(3);
  });
  it.each([0, 1, 2, 3])('revokes authority after prerequisite %s', async delayed => {
    const scope = new InventoryOperationScope();
    const prerequisites = [response({ status: { allowed: true } }), response(original), response({ metadata: { uid: 'facility' } }), response({ metadata: { uid: 'rack' }, spec: { facilityRef: { name: 'facility' }, heightUnits: 42 } })];
    let resolve!: (value: unknown) => void;
    let index = 0;
    const fetch = vi.fn().mockImplementation(() => index++ === delayed ? new Promise(done => { resolve = done; }) : Promise.resolve(prerequisites[index - 1]));
    vi.stubGlobal('fetch', fetch);
    const operation = correctDevicePlacement(original, { ...fields, facility: 'facility', rack: 'rack' }, scope.begin());
    await vi.waitFor(() => expect(resolve).toBeDefined());
    scope.invalidate(); resolve(prerequisites[delayed]);
    await expect(operation).rejects.toMatchObject({ dispatched: false });
    expect(fetch).toHaveBeenCalledTimes(delayed + 1);
  });
  it('keeps a dispatched result unknown across session change', async () => {
    const scope = new InventoryOperationScope();
    let resolve!: (value: unknown) => void;
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(original)).mockImplementationOnce(() => new Promise(done => { resolve = done; }));
    vi.stubGlobal('fetch', fetch);
    const operation = correctDevicePlacement(original, fields, scope.begin());
    await vi.waitFor(() => expect(resolve).toBeDefined());
    scope.invalidate(); resolve(response(original));
    await expect(operation).rejects.toMatchObject({ dispatched: true });
    expect(fetch).toHaveBeenCalledTimes(3);
  });
  it.each([401, 403, 404, 503])('denied location %s never dispatches', async status => {
    const fetch = vi.fn().mockResolvedValueOnce(response({ status: { allowed: true } })).mockResolvedValueOnce(response(original)).mockResolvedValueOnce(response({}, status));
    vi.stubGlobal('fetch', fetch);
    await expect(correctDevicePlacement(original, { ...fields, rack: 'rack' }, new InventoryOperationScope().begin())).rejects.toMatchObject({ kind: 'Rack', status });
    expect(fetch).toHaveBeenCalledTimes(3);
  });
});
