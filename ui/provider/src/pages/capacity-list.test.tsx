import CapacityList from './capacity-list';
import { capacityTargets, toRawCapacity } from '../components/create-capacity-dialog';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_CAPACITIES, fixtureClasses } from '../lib/fixtures';
import { toMachineCapacity, toMachineClass } from '../types';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual<typeof api>('../lib/api');
  return { ...actual, useMachineCapacities: vi.fn(), useMachineClasses: vi.fn(), useCreateMachineCapacity: vi.fn(), useDeleteMachineCapacity: vi.fn() };
});

const useCapacitiesMock = vi.mocked(api.useMachineCapacities);
const mutate = vi.fn();
const CLASSES = fixtureClasses.list().map(toMachineClass);

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

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/plugins/metal-datumapis-com']}>
        <Routes>
          <Route path="/plugins/:slug" element={<CapacityList />} />
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
  mutate.mockReset();
  remove.mockReset();
  mockDelete(api.useDeleteMachineCapacity);
  vi.mocked(api.useMachineClasses).mockReturnValue({ data: CLASSES } as never);
  vi.mocked(api.useCreateMachineCapacity).mockReturnValue({
    mutate,
    reset: vi.fn(),
    error: null,
    isPending: false,
  } as never);
});

describe('CapacityList', () => {
  it('names the provider project', () => {
    mockResult({ data: [] });
    renderPage();
    expect(screen.getByText('Provider resources in datum-cloud')).toBeInTheDocument();
  });

  it('keeps Capacity active at the plugin root', () => {
    mockResult({ data: [] });
    renderPage();
    expect(screen.getByRole('tab', { name: 'Capacity' })).toHaveAttribute('data-state', 'active');
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

  it('lists the fixture capacity with policy and conditions', () => {
    mockResult({ data: FIXTURE_CAPACITIES.map(toMachineCapacity) });
    renderPage();
    const table = screen.getByTestId('fleet-plugin-capacity-table');

    const free = row(table, 'central1-baremetal-01');
    expect(within(free).getByText('fleet-provider')).toBeInTheDocument();
    expect(within(free).getByText('Enabled')).toBeInTheDocument();
    expect(within(free).getAllByText('Yes')).toHaveLength(3);

    const allocated = row(table, 'central1-baremetal-02');
    expect(within(allocated).getAllByText('Yes')).toHaveLength(2);
    expect(within(allocated).getByText('No')).toBeInTheDocument();

    const enrolled = row(table, 'central1-baremetal-03');
    expect(within(enrolled).getByText('Disabled')).toBeInTheDocument();
    expect(within(enrolled).queryByText('Yes')).toBeNull();
    expect(within(enrolled).getAllByText('—').length).toBeGreaterThan(0); // nothing observed
  });

  it('creates capacity in the namespace and pool of the chosen class', async () => {
    mockResult({ data: [] });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /Create capacity/ }));
    const form = await screen.findByTestId('fleet-plugin-create-capacity-form');
    const input = (name: string) => form.querySelector(`[name="${name}"]`) as HTMLInputElement;
    fireEvent.change(input('name'), { target: { value: 'central1-baremetal-04' } });
    // Radix renders a hidden native select alongside its combobox.
    fireEvent.change(form.querySelector('select') as HTMLSelectElement, {
      target: { value: 'standard-metal' },
    });
    expect(await within(form).findByTestId('fleet-plugin-capacity-placement')).toHaveTextContent(
      'Created in namespace fleet-provider with capacity pool central-metal.'
    );
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(mutate).toHaveBeenCalled());
    expect(mutate.mock.calls[0][0]).toEqual({
      metadata: {
        name: 'central1-baremetal-04',
        namespace: 'fleet-provider',
        labels: { 'fleet.datumapis.com/capacity-pool': 'central-metal' },
      },
      spec: {
        deviceRef: { name: 'central1-baremetal-04' },
        allocationPolicy: 'Disabled',
        cleaningPolicyRef: { name: 'synthetic-tenant-reuse-v1' },
        reclaimPolicy: 'Retain',
      },
    });
  });

  it('asks for a class with a capacity pool when there is none', async () => {
    vi.mocked(api.useMachineClasses).mockReturnValue({ data: [] } as never);
    mockResult({ data: [] });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /Create capacity/ }));
    const form = await screen.findByTestId('fleet-plugin-create-capacity-form');
    expect(within(form).getByText(/No classes with a capacity pool yet/)).toBeInTheDocument();
  });

  it('deletes capacity once its name is typed', async () => {
    mockResult({ data: FIXTURE_CAPACITIES.map(toMachineCapacity) });
    renderPage();
    const form = await confirmDelete('central1-baremetal-03');
    expect(within(form).getByText(/removed from fleet-provider straight away/)).toBeInTheDocument();
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

  it('shows a failed delete in the dialog', async () => {
    mockDelete(api.useDeleteMachineCapacity, { error: new ApiError(403, 'machinecapacities "x" is forbidden') });
    mockResult({ data: FIXTURE_CAPACITIES.map(toMachineCapacity) });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: 'Delete central1-baremetal-03' }));
    const form = await screen.findByTestId('fleet-plugin-delete-form');
    expect(within(form).getByText('machinecapacities "x" is forbidden')).toBeInTheDocument();
  });
});

describe('capacityTargets', () => {
  it('skips published classes and classes without a capacity pool', () => {
    const targets = capacityTargets([
      ...CLASSES,
      toMachineClass({ metadata: { name: 'no-pool' }, spec: {} }),
      toMachineClass({
        metadata: { name: 'published' },
        spec: { source: { project: 'provider', name: 'standard-metal' } },
      }),
    ]);
    expect(targets.map((t) => t.name)).toEqual(['standard-metal']);
  });
});

describe('toRawCapacity', () => {
  it('enables allocation only when asked', () => {
    const [target] = capacityTargets(CLASSES);
    const values = { name: 'm', machineClass: 'standard-metal' };
    expect(toRawCapacity(values, target).spec.allocationPolicy).toBe('Disabled');
    expect(toRawCapacity({ ...values, allocationEnabled: true }, target).spec.allocationPolicy).toBe(
      'Enabled'
    );
  });
});
