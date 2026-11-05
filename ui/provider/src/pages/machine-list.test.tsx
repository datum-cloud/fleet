import MachineList from './machine-list';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_MACHINES } from '../lib/fixtures';
import { toMachine } from '../types';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual<typeof api>('../lib/api');
  return { ...actual, useMachines: vi.fn() };
});

const useMachinesMock = vi.mocked(api.useMachines);

function mockResult(result: { data?: unknown; isLoading?: boolean; error?: unknown }) {
  useMachinesMock.mockReturnValue({
    data: undefined,
    isLoading: false,
    error: null,
    refetch: vi.fn(),
    ...result,
  } as never);
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/plugins/fleet-datumapis-com/machines']}>
        <Routes>
          <Route path="/plugins/:slug/machines" element={<MachineList />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

function row(table: HTMLElement, name: string): HTMLElement {
  const tr = within(table).getByText(name).closest('tr');
  expect(tr).not.toBeNull();
  return tr as HTMLElement;
}

beforeEach(() => {
  useMachinesMock.mockReset();
});

describe('MachineList', () => {
  it('names the provider project and namespace', () => {
    mockResult({ data: [] });
    renderPage();
    expect(screen.getByText('Provider capacity in datum-cloud / infra-ops')).toBeInTheDocument();
  });

  it('shows the loading state while fetching', () => {
    mockResult({ isLoading: true });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-loading')).toBeInTheDocument();
  });

  it('shows the restricted state on a 403', () => {
    mockResult({ error: new ApiError(403, 'forbidden') });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-restricted')).toBeInTheDocument();
  });

  it('shows a retryable error on other failures', () => {
    mockResult({ error: new ApiError(500, 'boom') });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-error')).toBeInTheDocument();
  });

  it('shows the empty state when there are no machines', () => {
    mockResult({ data: [] });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-machine-empty')).toBeInTheDocument();
  });

  it('lists the fixture machines with policy, conditions and provider', () => {
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();
    const table = screen.getByTestId('fleet-plugin-machine-table');

    const free = row(table, 'central1-baremetal-01');
    expect(within(free).getByText('Enabled')).toBeInTheDocument();
    expect(within(free).getAllByText('Yes')).toHaveLength(3);
    expect(within(free).getByText('Simulator')).toBeInTheDocument();

    const allocated = row(table, 'central1-baremetal-02');
    expect(within(allocated).getAllByText('Yes')).toHaveLength(2);
    expect(within(allocated).getByText('No')).toBeInTheDocument();

    const enrolled = row(table, 'central1-baremetal-03');
    expect(within(enrolled).getByText('Disabled')).toBeInTheDocument();
    expect(within(enrolled).queryByText('Yes')).toBeNull();
    expect(within(enrolled).getAllByText('—').length).toBeGreaterThan(0); // nothing observed
  });
});
