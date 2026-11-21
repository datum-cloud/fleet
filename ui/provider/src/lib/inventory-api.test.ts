import { afterEach, describe, expect, it, vi } from 'vitest';
import { fetchInventory, fetchInventoryCollection } from './api';

afterEach(() => vi.unstubAllGlobals());

function response(data: unknown, status = 200) {
  return { ok: status === 200, status, json: async () => ({ data }) };
}

describe('physical inventory reads', () => {
  it('reads every page with the project proxy and only GET', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(response({ items: [{ id: 1 }], metadata: { continue: 'opaque+/=' } })).mockResolvedValueOnce(response({ items: [{ id: 2 }] }));
    vi.stubGlobal('fetch', fetch);
    expect(await fetchInventoryCollection('racks')).toEqual([{ id: 1 }, { id: 2 }]);
    expect(fetch.mock.calls[1][0]).toContain('continue=opaque%2B%2F%3D');
    expect(fetch.mock.calls[0][0]).toContain('/projects/datum-cloud/control-plane/');
    expect(fetch.mock.calls.every((call) => call[1].method === 'GET')).toBe(true);
  });

  it('does not treat a pagination cycle as complete', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ items: [], metadata: { continue: 'repeat' } })));
    await expect(fetchInventoryCollection('racks')).rejects.toThrow('completeness Unknown');
  });

  it('reads all six collections and retains per-kind denial without fake inventory', async () => {
    const fetch = vi.fn().mockResolvedValue(response({ items: [] }));
    vi.stubGlobal('fetch', fetch);
    expect((await fetchInventory()).physical).toEqual([]);
    expect(fetch).toHaveBeenCalledTimes(6);
    fetch.mockResolvedValue(response(undefined, 403));
    const inventory = await fetchInventory();
    expect(Object.values(inventory.issues ?? {})).toEqual(Array.from({ length: 6 }, () => ({ status: 403 })));
    expect(inventory.physical).toEqual([]);
  });

  it('preserves accessible collections when another kind is denied', async () => {
    const facility = { kind: 'Facility', metadata: { name: 'example' }, spec: { displayName: 'Example' } };
    vi.stubGlobal('fetch', vi.fn((url: string) => Promise.resolve(url.includes('/facilities?') ? response({ items: [facility] }) : response(undefined, 403))));
    const inventory = await fetchInventory();
    expect(inventory.physical).toEqual([facility]);
    expect(inventory.issues?.Device).toEqual({ status: 403 });
    expect(inventory.issues?.Facility).toBeUndefined();
  });

  it('treats any session expiry as a global failure, never partial success', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => Promise.resolve(url.includes('/devices?') ? response(undefined, 401) : response({ items: [] }))));
    await expect(fetchInventory()).rejects.toMatchObject({ status: 401 });
  });

  it('distinguishes unavailable APIs and transient failures from empty collections', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => Promise.resolve(response(undefined, url.includes('/racks?') ? 404 : 503))));
    const inventory = await fetchInventory();
    expect(inventory.issues?.Rack?.status).toBe(404);
    expect(inventory.issues?.Device?.status).toBe(503);
  });
});
