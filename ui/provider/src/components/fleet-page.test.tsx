import { FleetPage } from './fleet-page';
import { PLUGIN_ID, PROVIDER_PROJECT } from '../lib/api';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';

const fetcher = vi.fn<typeof fetch>();
afterEach(() => { fetcher.mockReset(); vi.unstubAllGlobals(); });

function renderInventory(client?: QueryClient) {
  vi.stubGlobal('fetch', fetcher);
  const page = <MemoryRouter initialEntries={['/plugins/metal-datumapis-com/inventory']}><Routes><Route path="/plugins/:slug/inventory" element={<FleetPage testId="inventory"><p>Inventory</p></FleetPage>} /></Routes></MemoryRouter>;
  return render(client ? <QueryClientProvider client={client}>{page}</QueryClientProvider> : page);
}

it('shows cached counts on Inventory without reading any Bare Metal list', () => {
  const client = new QueryClient();
  client.setQueryData([PLUGIN_ID, 'machinecapacities', PROVIDER_PROJECT], [{}, {}]);
  renderInventory(client);
  expect(screen.getByRole('tab', { name: /^Capacity/ })).toHaveTextContent('Capacity2');
  expect(screen.getByRole('tab', { name: /^Profiles/ })).toHaveTextContent(/^Profiles$/);
  expect(screen.getByRole('tab', { name: /^Inventory/ })).toHaveAttribute('data-state', 'active');
  expect(screen.queryByRole('heading', { level: 1 })).toBeNull();
  expect(fetcher).not.toHaveBeenCalled();
});

it('renders the tabs without counts outside a QueryClient', () => {
  renderInventory();
  expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['Capacity', 'Profiles', 'Classes', 'Inventory']);
  expect(fetcher).not.toHaveBeenCalled();
});
