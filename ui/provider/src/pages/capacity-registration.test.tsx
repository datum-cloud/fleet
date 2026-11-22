import CapacityList from './capacity-list';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

afterEach(() => vi.unstubAllGlobals());

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <CapacityList />
      </MemoryRouter>
    </QueryClientProvider>
  );
}

function response(status: number) {
  return { ok: status === 200, status, json: async () => ({ data: { items: [] }, error: 'Unavailable' }) };
}

describe('blocked registration through the production UI API path', () => {
  it('only reads capacity, including simultaneous page mounts', async () => {
    const request = vi.fn().mockResolvedValue(response(200));
    vi.stubGlobal('fetch', request);
    mount();
    mount();
    await waitFor(() => expect(screen.getAllByTestId('fleet-plugin-capacity-empty')).toHaveLength(2));
    expect(request).toHaveBeenCalledTimes(2);
    for (const [url, options] of request.mock.calls) {
      expect(url).toBe('/api/internal/apis/resourcemanager.miloapis.com/v1alpha1/projects/datum-cloud/control-plane/apis/metal.datumapis.com/v1alpha1/machinecapacities?limit=100');
      expect(options.method).toBe('GET');
      expect(options.body).toBeUndefined();
    }
    expect(screen.queryByRole('button', { name: /Create|Register/ })).toBeNull();
  });

  it.each([401, 403, 503])('never dispatches registration after a %s read failure', async (status) => {
    const request = vi.fn().mockResolvedValueOnce(response(status)).mockResolvedValue(response(200));
    vi.stubGlobal('fetch', request);
    const view = mount();
    await screen.findByTestId(status === 403 ? 'fleet-plugin-restricted' : 'fleet-plugin-error');
    expect(screen.getByTestId('fleet-plugin-capacity-registration-blocked')).toBeInTheDocument();
    if (status !== 403) {
      fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    } else {
      view.unmount();
      mount();
    }
    await screen.findByTestId('fleet-plugin-capacity-empty');
    expect(request).toHaveBeenCalledTimes(2);
    expect(request.mock.calls.every(([, options]) => options.method === 'GET')).toBe(true);
    expect(screen.queryByRole('button', { name: /Create|Register/ })).toBeNull();
  });
});
