import DeviceClaimDetail from './device-claim-detail';
import DeviceInventoryList from './device-inventory-list';
import { toInventory } from '../inventory';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { fixtureDeviceClaims, fixtureDevices } from '../lib/fixtures';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, within } from '@testing-library/react';
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
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/plugins/:slug/inventory" element={<DeviceInventoryList />} />
          <Route path="/plugins/:slug/inventory/:namespace/:claim" element={<DeviceClaimDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

const inventory = toInventory(fixtureDeviceClaims, fixtureDevices);

beforeEach(() => useInventoryMock.mockReset());

describe('DeviceInventoryList', () => {
  it('marks the Inventory tab active', () => {
    mockInventory({ data: inventory });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(screen.getByRole('tab', { name: 'Inventory' })).toHaveAttribute('data-state', 'active');
  });

  it('lists claims with their bound Device and links to the detail page', () => {
    mockInventory({ data: inventory });
    renderAt('/plugins/metal-datumapis-com/inventory');
    const table = screen.getByTestId('fleet-plugin-inventory-table');
    expect(within(table).getByText('rack-01-u12-synthetic-0001')).toBeInTheDocument();
    expect(within(table).getByText('SYNTHETIC-0001')).toBeInTheDocument();
    expect(within(table).getByText('Not registered')).toBeInTheDocument();
    expect(within(table).getByRole('link', { name: 'rack-01-u12' })).toHaveAttribute(
      'href',
      '/plugins/metal-datumapis-com/inventory/default/rack-01-u12'
    );
  });

  it('shows Devices whose claim is missing', () => {
    mockInventory({ data: inventory });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(within(screen.getByTestId('fleet-plugin-unbound-devices')).getByText(/orphan-synthetic-0009/)).toBeInTheDocument();
  });

  it('shows the restricted state on a 403', () => {
    mockInventory({ error: new ApiError(403, 'forbidden') });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(screen.getByTestId('fleet-plugin-restricted')).toBeInTheDocument();
  });

  it('shows the empty state with no records', () => {
    mockInventory({ data: toInventory([], []) });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(screen.getByTestId('fleet-plugin-inventory-empty')).toBeInTheDocument();
  });

  it('offers no write actions', () => {
    mockInventory({ data: inventory });
    renderAt('/plugins/metal-datumapis-com/inventory');
    expect(screen.queryByRole('button', { name: /create|delete|edit/i })).not.toBeInTheDocument();
  });
});

describe('DeviceClaimDetail', () => {
  it('shows placement, ports, binding, identity, BMC endpoint, unmapped MACs and provenance', () => {
    mockInventory({ data: inventory });
    renderAt('/plugins/metal-datumapis-com/inventory/default/rack-01-u12');
    const claim = screen.getByTestId('fleet-plugin-claim');
    expect(within(claim).getByText('example-facility · rack-01 · U12 front')).toBeInTheDocument();
    expect(within(claim).getByText(/eth0 \(25gbase-x-sfp28, 25000 Mb\/s\) → switch-01 Ethernet1\/12/)).toBeInTheDocument();
    expect(within(claim).getByText('planner@example.com')).toBeInTheDocument();
    const device = screen.getByTestId('fleet-plugin-device-rack-01-u12-synthetic-0001');
    for (const text of ['SYNTHETIC-0001', '00000000-0000-4000-8000-000000000001', 'https://192.0.2.100:443', 'rack-01-u12', 'registrar@example.com', 'BMC inventory read during rack turn-up']) {
      expect(within(device).getByText(text)).toBeInTheDocument();
    }
    const macs = within(device).getByTestId('fleet-plugin-unmapped-macs');
    expect(within(macs).getByText('00:00:5e:00:53:01')).toBeInTheDocument();
    expect(within(macs).getByText('00:00:5e:00:53:02')).toBeInTheDocument();
  });

  it('shows an unregistered slot', () => {
    mockInventory({ data: inventory });
    renderAt('/plugins/metal-datumapis-com/inventory/default/rack-01-u10');
    expect(within(screen.getByTestId('fleet-plugin-claim')).getByText('Not registered')).toBeInTheDocument();
  });

  it('reports a missing claim', () => {
    mockInventory({ data: inventory });
    renderAt('/plugins/metal-datumapis-com/inventory/default/nope');
    expect(screen.getByTestId('fleet-plugin-claim-missing')).toBeInTheDocument();
  });

  it('lists earlier hardware after the current Device', () => {
    const replacement = {
      ...fixtureDevices[0],
      metadata: { ...fixtureDevices[0].metadata, name: 'rack-01-u12-synthetic-0002' },
      spec: { ...fixtureDevices[0].spec, provenance: { ...fixtureDevices[0].spec.provenance, recordedAt: '2026-02-01T00:00:00Z' } },
    };
    mockInventory({ data: toInventory(fixtureDeviceClaims, [fixtureDevices[0], replacement]) });
    renderAt('/plugins/metal-datumapis-com/inventory/default/rack-01-u12');
    expect(within(screen.getByTestId('fleet-plugin-claim')).getByText('rack-01-u12-synthetic-0002')).toBeInTheDocument();
    expect(within(screen.getByTestId('fleet-plugin-device-rack-01-u12-synthetic-0001')).getByText('Earlier hardware for this slot')).toBeInTheDocument();
  });
});
