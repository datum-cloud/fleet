import DeviceInventoryList from './device-inventory-list';
import { toInventory } from '../inventory';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { InventoryPreviewContext } from '../components/inventory-session-boundary';
import { fixtureDeviceClaims, fixtureDevices } from '../lib/fixtures';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual<typeof api>('../lib/api');
  return { ...actual, useInventory: vi.fn() };
});

const useInventoryMock = vi.mocked(api.useInventory);

function mockInventory(result: { data?: unknown; isLoading?: boolean; error?: unknown }) {
  useInventoryMock.mockReturnValue({ data: undefined, isLoading: false, error: null, refetch: vi.fn(), ...result } as never);
}

function renderAt(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <InventoryPreviewContext.Provider value={true}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/plugins/:slug/inventory" element={<DeviceInventoryList />} />
        </Routes>
      </MemoryRouter>
      </InventoryPreviewContext.Provider>
    </QueryClientProvider>
  );
}

const inventory = toInventory(fixtureDeviceClaims, fixtureDevices);

beforeEach(() => useInventoryMock.mockReset());

describe('DeviceInventoryList', () => {
  it('keeps manufacturer deep links in loading state without claiming empty relationships', () => {
    mockInventory({ isLoading: true });
    renderAt('/plugins/metal-datumapis-com/inventory?kind=Manufacturer&namespace=default&record=private-vendor');
    expect(screen.getByTestId('fleet-plugin-loading')).toBeInTheDocument();
    expect(screen.queryByText(/No device types linked|No devices explicitly linked|private-vendor/)).not.toBeInTheDocument();
  });

  it('marks the Inventory tab active', () => {
    mockInventory({ data: inventory });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(screen.getByRole('tab', { name: 'Inventory' })).toHaveAttribute('data-state', 'active');
  });

  it('starts with physical devices and links to their own identity', () => {
    mockInventory({ data: inventory });
    renderAt('/plugins/metal-datumapis-com/inventory');
    const table = screen.getByTestId('fleet-plugin-inventory-table');
    expect(within(table).getByText('rack-01-u12-synthetic-0001')).toBeInTheDocument();
    expect(within(table).getByText('SYNTHETIC-0001')).toBeInTheDocument();
    expect(within(table).getByRole('link', { name: 'rack-01-u12-synthetic-0001' })).toHaveAttribute(
      'href',
      '/plugins/metal-datumapis-com/inventory?kind=Device&record=rack-01-u12-synthetic-0001&namespace=default'
    );
  });

  it('shows Devices whose claim is missing', () => {
    mockInventory({ data: inventory });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(within(screen.getByTestId('fleet-plugin-inventory-table')).getByText(/orphan-synthetic-0009/)).toBeInTheDocument();
    expect(screen.getByText('Claim record not found')).toBeInTheDocument();
  });

  it('shows the restricted state on a 403', () => {
    mockInventory({ error: new ApiError(403, 'forbidden') });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(screen.getByTestId('inventory-access-403')).toBeInTheDocument();
  });

  it('handles an expired session without exposing cached inventory', () => {
    mockInventory({ data: inventory, error: new ApiError(401, 'Private backend detail') });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(screen.getByRole('heading', { name: 'Your session has expired' })).toBeInTheDocument();
    expect(screen.queryByText('SYNTHETIC-0001')).not.toBeInTheDocument();
    expect(screen.queryByText('Private backend detail')).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Return to staff portal' })).toHaveAttribute('href', '/');
  });

  it('does not show record counts before loading completes', () => {
    mockInventory({ isLoading: true });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(screen.getByTestId('fleet-plugin-loading')).toBeInTheDocument();
    expect(screen.getByRole('navigation', { name: 'Inventory categories' }).textContent).not.toMatch(/\d/);
  });

  it('hides stale counts on a failed read and allows retry', () => {
    mockInventory({ data: inventory, error: new ApiError(500, 'Inventory unavailable') });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(screen.getByTestId('inventory-access-500')).toBeInTheDocument();
    expect(screen.getByRole('navigation', { name: 'Inventory categories' }).textContent).not.toMatch(/\d/);
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(useInventoryMock.mock.results[0].value.refetch).toHaveBeenCalledOnce();
  });

  it('shows the empty state with no records', () => {
    mockInventory({ data: toInventory([], []) });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(screen.getByTestId('fleet-plugin-inventory-empty')).toBeInTheDocument();
  });

  it('offers no write actions', () => {
    mockInventory({ data: inventory });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(screen.getByRole('button', { name: /create/i })).toBeDisabled();
    expect(screen.queryByRole('button', { name: /delete|edit/i })).not.toBeInTheDocument();
  });
});
