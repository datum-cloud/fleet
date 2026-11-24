import CapacityList from './capacity-list';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_CAPACITIES } from '../lib/fixtures';
import { toMachineCapacity } from '../types';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual<typeof api>('../lib/api');
  return { ...actual, useMachineCapacities: vi.fn(), useMachineProfiles: vi.fn(), useMachineClasses: vi.fn(), useDeleteMachineCapacity: vi.fn() };
});

const useCapacitiesMock = vi.mocked(api.useMachineCapacities);

const remove = vi.fn();

function mockDelete(hook: unknown, result: { error?: unknown } = {}) {
  vi.mocked(hook as () => unknown).mockReturnValue({
    mutate: remove,
    reset: vi.fn(),
    error: null,
    isPending: false,
    ...result,
  } as never);
}

async function confirmDelete(name: string, typed = name) {
  fireEvent.click(screen.getByRole('button', { name: `Delete ${name}` }));
  const form = await screen.findByTestId('fleet-plugin-delete-form');
  fireEvent.change(form.querySelector('input[name="confirm"]') as HTMLInputElement, {
    target: { value: typed },
  });
  fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
  return form;
}


function mockResult(result: { data?: unknown; isLoading?: boolean; error?: unknown }) {
  useCapacitiesMock.mockReturnValue({
    data: undefined,
    isLoading: false,
    error: null,
    refetch: vi.fn(),
    ...result,
  } as never);
}

function renderPage(entry = '/plugins/metal-datumapis-com') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/plugins/:slug" element={<CapacityList />} />
          <Route path="/plugins/:slug/inventory" element={<p>Inventory route</p>} />
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
  useCapacitiesMock.mockReset();
  remove.mockReset();
  mockDelete(api.useDeleteMachineCapacity);
  vi.mocked(api.useMachineProfiles).mockReturnValue({ data: [{ name: 'p' }], error: null } as never);
  vi.mocked(api.useMachineClasses).mockReturnValue({ data: undefined, error: new ApiError(403, 'forbidden') } as never);
});

describe('CapacityList', () => {
  it('heads the page with the tab row alone, the action at its end', () => {
    mockResult({ data: [] });
    renderPage();
    expect(screen.getByRole('heading', { level: 1, name: 'Bare Metal' })).toBeInTheDocument();
    expect(screen.queryByText(/Provider resources in/)).toBeNull();
    const row = screen.getByRole('tablist').parentElement!.parentElement!;
    expect(row).toHaveClass('fleet-tab-row');
    expect(within(row).getByRole('button', { name: 'Register capacity' })).toBeDisabled();
  });

  it('keeps Capacity active at the plugin root', () => {
    mockResult({ data: [] });
    renderPage();
    expect(screen.getByRole('tab', { name: /^Capacity/ })).toHaveAttribute('data-state', 'active');
  });

  it('counts each list on its tab, with ? for an unreadable one and none while loading', () => {
    mockResult({ data: FIXTURE_CAPACITIES.map(toMachineCapacity) });
    renderPage();
    expect(screen.getByRole('tab', { name: /^Capacity/ })).toHaveTextContent('Capacity3');
    expect(screen.getByRole('tab', { name: /^Profiles/ })).toHaveTextContent('Profiles1');
    expect(screen.getByRole('tab', { name: /^Classes/ })).toHaveTextContent('Classes?');
    expect(screen.getByRole('tab', { name: /^Inventory/ })).toHaveTextContent(/^Inventory$/);
  });

  it('shows no capacity count while the list loads or after it fails', () => {
    mockResult({ isLoading: true });
    const { unmount } = renderPage();
    expect(screen.getByRole('tab', { name: /^Capacity/ })).toHaveTextContent(/^Capacity$/);
    unmount();
    mockResult({ error: new ApiError(403, 'forbidden') });
    renderPage();
    expect(screen.getByRole('tab', { name: /^Capacity/ })).toHaveTextContent('Capacity?');
  });

  it('shows search only for a list long enough to need it', () => {
    mockResult({ data: FIXTURE_CAPACITIES.map(toMachineCapacity) });
    const { unmount } = renderPage();
    expect(screen.queryByPlaceholderText('Search machine capacity')).toBeNull();
    unmount();
    const many = Array.from({ length: 10 }, (_, i) => ({ ...toMachineCapacity(FIXTURE_CAPACITIES[0]), name: `capacity-${i}` }));
    mockResult({ data: many });
    renderPage();
    expect(screen.getByPlaceholderText('Search machine capacity')).toBeInTheDocument();
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

  it('shows the empty state when there is no capacity', () => {
    mockResult({ data: [] });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-capacity-empty')).toBeInTheDocument();
  });

  it('lists the fixture capacity with one state word, its Device and policy', () => {
    mockResult({ data: FIXTURE_CAPACITIES.map(toMachineCapacity) });
    renderPage();
    const table = screen.getByTestId('fleet-plugin-capacity-table');
    expect(within(table).queryByText('Namespace')).toBeNull();
    expect(within(table).queryByText('fleet-provider')).toBeNull();
    expect(within(table).getAllByRole('columnheader').map((th) => th.textContent).slice(0, 3)).toEqual(['Name', 'State', 'Device']);

    const free = row(table, 'central1-baremetal-01');
    expect(within(free).getByLabelText('Available: Prepared True (SyntheticEvidence) · Healthy True (SyntheticEvidence) · Available True (SyntheticEvidence)')).not.toHaveAttribute('data-attention');
    expect(within(free).getByText('Enabled')).toBeInTheDocument();
    expect(within(free).getByText('synthetic-device-central-01')).toBeInTheDocument();
    expect(within(free).queryByText(/^(Yes|No)$/)).toBeNull();

    const allocated = row(table, 'central1-baremetal-02');
    expect(within(allocated).getByLabelText(/^Allocated: .*Available False \(Allocated\)$/)).not.toHaveAttribute('data-attention');

    const enrolled = row(table, 'central1-baremetal-03');
    expect(within(enrolled).getByLabelText(/^Disabled: Prepared Unknown · Healthy Unknown · Available Unknown$/)).not.toHaveAttribute('data-attention');
    expect(within(enrolled).getAllByText('—').length).toBeGreaterThan(0); // nothing observed
  });

  it('colours only capacity that needs attention, and never reads unknown as available', () => {
    const base = toMachineCapacity(FIXTURE_CAPACITIES[0]);
    mockResult({ data: [
      { ...base, name: 'sick', healthy: { state: 'False', reason: 'BMCUnreachable' } },
      { ...base, name: 'unreported', prepared: { state: 'Unknown' } },
    ] });
    renderPage();
    const table = screen.getByTestId('fleet-plugin-capacity-table');
    expect(within(row(table, 'sick')).getByLabelText(/^Unhealthy: .*Healthy False \(BMCUnreachable\)/)).toHaveAttribute('data-attention', 'conflict');
    const unknown = within(row(table, 'unreported')).getByLabelText(/^Unknown: Prepared Unknown/);
    expect(unknown).toHaveTextContent('?');
    expect(unknown).toHaveAttribute('data-attention', 'unavailable');
  });

  it('links a Device in the inventory namespace to its inventory record', () => {
    const base = toMachineCapacity(FIXTURE_CAPACITIES[0]);
    mockResult({ data: [base, { ...base, name: 'device-uid-1', namespace: 'default', device: 'server-a' }, { ...base, name: 'unbacked', device: undefined }] });
    renderPage();
    const table = screen.getByTestId('fleet-plugin-capacity-table');
    expect(within(row(table, 'device-uid-1')).getByRole('link', { name: 'server-a' })).toHaveAttribute('href', '/plugins/metal-datumapis-com/inventory?kind=Device&record=server-a&namespace=default');
    // Inventory holds only the default namespace, so other Devices are named, not linked.
    expect(within(row(table, 'central1-baremetal-01')).getByText('synthetic-device-central-01').closest('a')).toBeNull();
    expect(within(row(table, 'unbacked')).queryByRole('link')).toBeNull();
  });

  it('filters to the pool a class links to', () => {
    const base = toMachineCapacity(FIXTURE_CAPACITIES[0]);
    mockResult({ data: [...FIXTURE_CAPACITIES.map(toMachineCapacity), { ...base, name: 'other-pool', pool: 'edge-metal' }, { ...base, name: 'other-namespace', namespace: 'default' }] });
    renderPage('/plugins/metal-datumapis-com?namespace=fleet-provider&pool=central-metal');
    const table = screen.getByTestId('fleet-plugin-capacity-table');
    expect(within(table).getAllByRole('row')).toHaveLength(4);
    expect(within(table).queryByText('other-pool')).toBeNull();
    expect(within(table).queryByText('other-namespace')).toBeNull();
    expect(screen.getByTestId('fleet-plugin-capacity-filter')).toHaveTextContent('Pool central-metal');
    expect(screen.getByRole('tab', { name: /^Capacity/ })).toHaveTextContent('Capacity5');
    fireEvent.click(screen.getByRole('link', { name: 'Show all' }));
    expect(within(screen.getByTestId('fleet-plugin-capacity-table')).getAllByRole('row')).toHaveLength(6);
  });

  it('says a filtered pool is empty rather than that no capacity exists', () => {
    mockResult({ data: FIXTURE_CAPACITIES.map(toMachineCapacity) });
    renderPage('/plugins/metal-datumapis-com?pool=missing');
    expect(screen.getByTestId('fleet-plugin-capacity-empty')).toHaveTextContent('No machine capacity in this pool');
  });

  it.each([
    { data: [] },
    { data: FIXTURE_CAPACITIES.map(toMachineCapacity) },
    { isLoading: true },
    { error: new ApiError(403, 'forbidden') },
    { error: new ApiError(503, 'unavailable') },
  ])('does not offer registration without a verified host session: %j', (result) => {
    mockResult(result);
    renderPage();
    // The trigger is disabled and opens nothing; the reason is its description.
    const trigger = screen.getByRole('button', { name: 'Register capacity' });
    expect(trigger).toBeDisabled();
    expect(trigger).toHaveAccessibleDescription('Registration unavailable: a verified staff session is required.');
    fireEvent.click(trigger);
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.queryByRole('button', { name: /Create capacity|Register Disabled capacity/ })).toBeNull();
    expect(screen.queryByRole('checkbox', { name: /Offer for allocation/ })).toBeNull();
    expect(screen.queryByTestId('fleet-plugin-create-capacity-form')).toBeNull();
    expect(remove).not.toHaveBeenCalled();
  });

  it('deletes capacity once its name is typed', async () => {
    mockResult({ data: FIXTURE_CAPACITIES.map(toMachineCapacity) });
    renderPage();
    const form = await confirmDelete('central1-baremetal-03');
    expect(within(form).getByText(/Deletion in fleet-provider is subject to server lifecycle fences/)).toBeInTheDocument();
    await waitFor(() => expect(remove).toHaveBeenCalled());
    expect(remove.mock.calls[0][0]).toMatchObject({
      name: 'central1-baremetal-03',
      namespace: 'fleet-provider',
    });
  });

  it('does not delete when the typed name is wrong', async () => {
    mockResult({ data: FIXTURE_CAPACITIES.map(toMachineCapacity) });
    renderPage();
    const form = await confirmDelete('central1-baremetal-03', 'central1-baremetal-0');
    expect(await within(form).findByText('Type central1-baremetal-03 to confirm.')).toBeInTheDocument();
    expect(remove).not.toHaveBeenCalled();
  });

  it('shows registration retention instead of an unsafe rollback action', () => {
    mockResult({ data: [{ ...toMachineCapacity(FIXTURE_CAPACITIES[0]), registrationProtected: true }] });
    renderPage();
    expect(screen.getByText('Retained registration')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: `Delete ${FIXTURE_CAPACITIES[0].metadata.name}` })).toBeNull();
  });

  it('shows a failed delete in the dialog', async () => {
    mockDelete(api.useDeleteMachineCapacity, { error: new ApiError(403, 'machinecapacities "x" is forbidden') });
    mockResult({ data: FIXTURE_CAPACITIES.map(toMachineCapacity) });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: 'Delete central1-baremetal-03' }));
    const form = await screen.findByTestId('fleet-plugin-delete-form');
    expect(within(form).getByText('machinecapacities "x" is forbidden')).toBeInTheDocument();
  });
});
