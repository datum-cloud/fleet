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

  it('requires all six collections and propagates restricted errors', async () => {
    const fetch = vi.fn().mockResolvedValue(response({ items: [] }));
    vi.stubGlobal('fetch', fetch);
    expect((await fetchInventory()).physical).toEqual([]);
    expect(fetch).toHaveBeenCalledTimes(6);
    fetch.mockResolvedValue(response(undefined, 403));
    await expect(fetchInventory()).rejects.toMatchObject({ status: 403 });
  });
});
