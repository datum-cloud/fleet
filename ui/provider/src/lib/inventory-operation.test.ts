import { afterEach, describe, expect, it, vi } from 'vitest';
import { waitFor } from '@testing-library/react';
import { createInventoryRecord, editInventoryLabel, inventoryPayload, INVENTORY_FIELD_MANAGER } from './inventory-management';
import { InventoryOperationInvalidated, InventoryOperationScope } from './inventory-operation';

const values = { name: 'synthetic-facility', namespace: 'default', displayName: 'Original', recordedBy: 'synthetic-operator', reference: '', facility: 'parent', height: '' };
const original = { ...inventoryPayload('Facility', values, '2026-01-01T00:00:00Z'), metadata: { name: values.name, namespace: values.namespace, uid: 'synthetic-uid', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } };
const response = (data: unknown, status = 200) => ({ ok: status < 400, status, json: async () => ({ data }) });
afterEach(() => vi.unstubAllGlobals());

describe('imperative inventory operation authority', () => {
  it.each(['facility-review', 'rack-review', 'rack-get', 'edit-review', 'edit-get'])('stops after invalidation during %s without dispatching a write', async stage => {
    const scope = new InventoryOperationScope();
    let release: ((value: unknown) => void) | undefined;
    const requests = vi.fn(async (url: string, init: RequestInit) => {
      const review = url.includes('selfsubjectaccessreviews');
      if ((stage.endsWith('review') && review) || (stage.endsWith('get') && init.method === 'GET')) {
        return new Promise(resolve => { release = resolve; });
      }
      return response(review ? { status: { allowed: true } } : original);
    });
    vi.stubGlobal('fetch', requests);
    const pending = stage.startsWith('edit')
      ? editInventoryLabel(original, values, scope.begin())
      : createInventoryRecord(stage.startsWith('rack') ? 'Rack' : 'Facility', values, scope.begin());
    const outcome = pending.catch(error => error);
    await waitFor(() => expect(release).toBeDefined());
    scope.invalidate();
    release!(response(stage.endsWith('review') ? { status: { allowed: true } } : original));
    expect(await outcome).toBeInstanceOf(InventoryOperationInvalidated);
    expect(await outcome).toMatchObject({ dispatched: false });
    expect(requests.mock.calls.filter(([url, init]) => !url.includes('selfsubjectaccessreviews') && init.method !== 'GET')).toEqual([]);
  });

  it.each(['create-success', 'create-error', 'edit-success', 'edit-error'])('keeps invalidated dispatched %s outcomes unknown and never retries', async mode => {
    const scope = new InventoryOperationScope();
    let release: ((value: unknown) => void) | undefined;
    const requests = vi.fn(async (url: string, init: RequestInit) => {
      if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
      if (init.method === 'GET') return response(original);
      return new Promise(resolve => { release = resolve; });
    });
    vi.stubGlobal('fetch', requests);
    const outcome = (mode.startsWith('edit')
      ? editInventoryLabel(original, values, scope.begin())
      : createInventoryRecord('Facility', values, scope.begin())).catch(error => error);
    await waitFor(() => expect(release).toBeDefined());
    scope.invalidate();
    release!(response(original, mode.endsWith('error') ? 503 : 200));
    expect(await outcome).toBeInstanceOf(InventoryOperationInvalidated);
    expect(await outcome).toMatchObject({ dispatched: true });
    expect((await outcome).message).toMatch(/outcome is unknown/);
    expect(requests.mock.calls.filter(([url, init]) => !url.includes('selfsubjectaccessreviews') && init.method !== 'GET')).toHaveLength(1);
  });

  it('never revives earlier operation generations when new sessions start', async () => {
    const requests = vi.fn();
    vi.stubGlobal('fetch', requests);
    const first = new InventoryOperationScope();
    const pending = first.begin();
    first.invalidate();
    const second = new InventoryOperationScope();
    const later = second.begin();
    second.invalidate();
    new InventoryOperationScope().begin().assertCurrent();
    await expect(createInventoryRecord('Facility', values, pending)).rejects.toMatchObject({ dispatched: false });
    await expect(editInventoryLabel(original, values, later)).rejects.toMatchObject({ dispatched: false });
    await expect(createInventoryRecord('Facility', values, first.begin())).rejects.toMatchObject({ dispatched: false });
    expect(requests).not.toHaveBeenCalled();
  });

  it('does not revive pending work when Strict Mode restarts a scope lifecycle', () => {
    const parent = new InventoryOperationScope();
    const child = new InventoryOperationScope();
    const old = child.begin(parent);
    parent.invalidate();
    parent.startLifecycle();
    expect(() => old.assertCurrent()).toThrow(InventoryOperationInvalidated);
    expect(() => child.begin(parent).assertCurrent()).not.toThrow();
  });
});
