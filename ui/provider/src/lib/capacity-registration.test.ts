import { beforeEach, expect, it, vi } from 'vitest';
import type { Device } from '../../../shared/fleet-api';
import { registerCapacity, ineligibleReason, REGISTRATION_DEVICE_UID, REGISTRATION_DEVICE_VERSION, RegistrationUnknown } from './capacity-registration';
import { InventoryOperationScope } from './inventory-operation';

const device: Device = { metadata: { name: 'chosen-hardware', namespace: 'default', uid: 'device-1', resourceVersion: '7' }, spec: { claimRef: { name: 'installation' }, identity: { model: 'test', serial: 'serial-1', systemUUID: '11111111-1111-1111-1111-111111111111' } } };
const transport = vi.fn<typeof fetch>();
beforeEach(() => {
  transport.mockReset(); vi.stubGlobal('fetch', transport);
  transport.mockImplementation(async (input, init) => {
    if (String(input).includes('selfsubjectaccessreviews')) return Response.json({ data: { status: { allowed: true } } });
    if (init?.method === 'POST') { const body = JSON.parse(String(init.body)); return Response.json({ data: { ...body, metadata: { ...body.metadata, uid: 'capacity-1' } } }); }
    return Response.json({ data: device });
  });
});

it('uses the exact selected Device, authored name, Disabled/Retain and no synthetic policy', async () => {
  const result = await registerCapacity('authored-name', device, 'operation-1', new InventoryOperationScope().begin());
  expect(result.spec).toEqual({ deviceRef: { name: 'chosen-hardware' }, allocationPolicy: 'Disabled', reclaimPolicy: 'Retain', cleaningPolicyRef: { name: '' } });
  expect(result.metadata.annotations).toMatchObject({ [REGISTRATION_DEVICE_UID]: 'device-1', [REGISTRATION_DEVICE_VERSION]: '7' });
  expect(result.status).toBeUndefined();
  const writes = transport.mock.calls.filter(([url, init]) => String(url).includes('machinecapacities?') && init?.method === 'POST');
  expect(writes).toHaveLength(1);
  expect(String(writes[0][0])).toContain('/projects/datum-cloud/control-plane/apis/metal.datumapis.com/v1alpha1/namespaces/default/machinecapacities');
});

it.each(['uid', 'resourceVersion'] as const)('rejects stale selected %s without refreshing or dispatching', async field => {
  transport.mockImplementation(async input => String(input).includes('selfsubjectaccessreviews') ? Response.json({ data: { status: { allowed: true } } }) : Response.json({ data: { ...device, metadata: { ...device.metadata, [field]: 'changed' } } }));
  await expect(registerCapacity('record', device, 'operation', new InventoryOperationScope().begin())).rejects.toThrow('selected Device changed');
  expect(transport.mock.calls.some(([url]) => String(url).includes('machinecapacities?'))).toBe(false);
});

it('does not dispatch on permission revocation', async () => {
  transport.mockResolvedValue(Response.json({ data: { status: { allowed: false } } }));
  await expect(registerCapacity('record', device, 'operation', new InventoryOperationScope().begin())).rejects.toThrow('permission is denied');
  expect(transport).toHaveBeenCalledTimes(1);
});

it('fences a session change before dispatch', async () => {
  const scope = new InventoryOperationScope();
  transport.mockImplementation(async () => { scope.invalidate(); return Response.json({ data: { status: { allowed: true } } }); });
  await expect(registerCapacity('record', device, 'operation', scope.begin())).rejects.toThrow('No write was dispatched');
  expect(transport).toHaveBeenCalledTimes(1);
});

it.each(['network', 'timeout', 'session', 'bad receipt'])('retains an unknown outcome after dispatch: %s', async failure => {
  const scope = new InventoryOperationScope();
  transport.mockImplementation(async (input, init) => {
    if (String(input).includes('selfsubjectaccessreviews')) return Response.json({ data: { status: { allowed: true } } });
    if (init?.method !== 'POST') return Response.json({ data: device });
    if (failure === 'network') throw new Error('network interrupted');
    if (failure === 'timeout') return Response.json({ error: 'timeout' }, { status: 504 });
    if (failure === 'session') scope.invalidate();
    return Response.json({ data: { metadata: {} } });
  });
  await expect(registerCapacity('record', device, 'operation', scope.begin())).rejects.toBeInstanceOf(RegistrationUnknown);
  expect(transport.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false);
});

it('rejects partial and unidentified identity and non-default inventory', () => {
  expect(ineligibleReason({ ...device, spec: { ...device.spec, identity: { model: 'test', serial: 'only-serial' } } })).toContain('ambiguous');
  expect(ineligibleReason({ ...device, metadata: { ...device.metadata, namespace: 'other' } })).toContain('unavailable');
});
