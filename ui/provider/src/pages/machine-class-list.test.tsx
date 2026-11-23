import MachineClassList from './machine-class-list';
import { toRawMachineClass } from '../components/create-class-dialog';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_CAPACITIES, fixtureClasses, fixtureProfiles } from '../lib/fixtures';
import { toMachineCapacity, toMachineClass, toMachineProfile } from '../types';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual<typeof api>('../lib/api');
  return {
    ...actual,
    useMachineClasses: vi.fn(),
    useMachineProfiles: vi.fn(),
    useMachineCapacities: vi.fn(),
    useCreateMachineClass: vi.fn(),
    useDeleteMachineClass: vi.fn(),
  };
});

const mutate = vi.fn();

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

function choosePool(form: HTMLElement, label: string) {
  fireEvent.click(within(form).getByRole('combobox'));
  fireEvent.click(screen.getByRole('option', { name: label }));
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

function mockClasses(result: { data?: unknown; isLoading?: boolean; error?: unknown }) {
  vi.mocked(api.useMachineClasses).mockReturnValue({
    data: undefined,
    isLoading: false,
    error: null,
    refetch: vi.fn(),
    ...result,
  } as never);
}

const CAPACITIES = FIXTURE_CAPACITIES.map(toMachineCapacity);

function mockCapacities(result: { data?: unknown; error?: unknown }) {
  vi.mocked(api.useMachineCapacities).mockReturnValue({ data: undefined, isLoading: false, error: null, ...result } as never);
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/plugins/metal-datumapis-com/classes']}>
        <Routes>
          <Route path="/plugins/:slug/classes" element={<MachineClassList />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

const PROFILES = [
  ...fixtureProfiles.list(),
  {
    metadata: { name: 'general-purpose-24c' },
    spec: { cpu: { architecture: 'amd64', cores: 24 }, memory: { capacity: '128Gi' } },
  },
].map(toMachineProfile);

beforeEach(() => {
  mutate.mockReset();
  remove.mockReset();
  mockDelete(api.useDeleteMachineClass);
  vi.mocked(api.useMachineProfiles).mockReturnValue({ data: PROFILES } as never);
  mockCapacities({ data: CAPACITIES });
  vi.mocked(api.useCreateMachineClass).mockReturnValue({
    mutate,
    reset: vi.fn(),
    error: null,
    isPending: false,
  } as never);
});

describe('MachineClassList', () => {
  it('activates the Classes tab', () => {
    mockClasses({ data: [] });
    renderPage();
    expect(screen.getByRole('tab', { name: /^Classes/ })).toHaveAttribute('data-state', 'active');
  });

  it('shows the restricted state on a 403', () => {
    mockClasses({ error: new ApiError(403, 'forbidden') });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-restricted')).toBeInTheDocument();
  });

  it('shows the empty state when there are no classes', () => {
    mockClasses({ data: [] });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-class-empty')).toBeInTheDocument();
  });

  it('lists classes with profiles, capacity and status', () => {
    mockClasses({ data: fixtureClasses.list().map(toMachineClass) });
    renderPage();
    const table = screen.getByTestId('fleet-plugin-class-table');
    const tr = within(table).getByText('standard-metal').closest('tr') as HTMLElement;
    expect(within(tr).getByText('general-purpose-24c-128g')).toBeInTheDocument();
    expect(within(tr).getByText('Accepted')).toBeInTheDocument();
    expect(within(tr).queryByText(/fleet-provider/)).toBeNull();
  });

  it('shows the pool with available / in-pool counts, linked to the filtered Capacity tab', () => {
    mockClasses({ data: fixtureClasses.list().map(toMachineClass) });
    renderPage();
    const tr = within(screen.getByTestId('fleet-plugin-class-table')).getByText('standard-metal').closest('tr') as HTMLElement;
    const pool = within(tr).getByRole('link', { name: 'central-metal: 1 available of 3 in pool central-metal (fleet-provider)' });
    expect(pool).toHaveTextContent('central-metal · 1/3');
    expect(pool).toHaveAttribute('href', '/plugins/metal-datumapis-com?namespace=fleet-provider&pool=central-metal');
    expect(within(pool).getByText('1/3')).not.toHaveAttribute('data-attention');
  });

  it('marks a pool with nothing available in amber', () => {
    mockCapacities({ data: CAPACITIES.filter((c) => c.name !== 'central1-baremetal-01') });
    mockClasses({ data: fixtureClasses.list().map(toMachineClass) });
    renderPage();
    expect(screen.getByText('0/2')).toHaveAttribute('data-attention', 'unavailable');
  });

  it('reads ? for pool counts when capacity cannot be read, never 0', () => {
    mockCapacities({ error: new ApiError(403, 'forbidden') });
    mockClasses({ data: fixtureClasses.list().map(toMachineClass) });
    renderPage();
    const pool = screen.getByRole('link', { name: /^central-metal: Capacity in fleet-provider could not be read/ });
    expect(pool).toHaveTextContent('central-metal · ?');
    expect(within(pool).getByText('?')).toHaveAttribute('data-attention', 'unavailable');
    expect(screen.queryByText(/0\/\d/)).toBeNull();
  });

  it('marks a class without an Accepted condition as not evaluated', async () => {
    mockClasses({
      data: [toMachineClass({ metadata: { name: 'standard-metal' }, spec: {} })],
    });
    renderPage();
    fireEvent.focus(screen.getByText('Not evaluated').closest('span') as HTMLElement);
    expect(await screen.findByRole('tooltip')).toHaveTextContent("Fleet hasn't evaluated this class yet.");
  });

  it('creates a class from the chosen profiles', async () => {
    mockClasses({ data: [] });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /Create class/ }));
    const form = await screen.findByTestId('fleet-plugin-create-class-form');
    const input = (name: string) => form.querySelector(`input[name="${name}"]`) as HTMLInputElement;
    fireEvent.change(input('name'), { target: { value: 'standard-metal' } });
    fireEvent.click(within(form).getByText('general-purpose-24c'));
    expect(form.querySelector('input[name="capacityPool"], input[name="capacityNamespace"]')).toBeNull();
    choosePool(form, 'central-metal · 3');
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(mutate).toHaveBeenCalled());
    expect(mutate.mock.calls[0][0]).toEqual(
      toRawMachineClass({
        name: 'standard-metal',
        allowedProfiles: ['general-purpose-24c'],
        capacity: 'fleet-provider/central-metal',
        powerControl: true,
      })
    );
    expect(mutate.mock.calls[0][0].spec.capacity.machineCapacities).toEqual({
      namespace: 'fleet-provider',
      selector: { matchLabels: { 'fleet.datumapis.com/capacity-pool': 'central-metal' } },
    });
  });

  it('offers only pools that capacity carries, and blocks creation without one', async () => {
    mockCapacities({ data: [...CAPACITIES, { ...CAPACITIES[0], name: 'unpooled', pool: undefined }, { ...CAPACITIES[0], name: 'edge', pool: 'edge-metal' }] });
    mockClasses({ data: [] });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /Create class/ }));
    const form = await screen.findByTestId('fleet-plugin-create-class-form');
    fireEvent.change(form.querySelector('input[name="name"]') as HTMLInputElement, { target: { value: 'c' } });
    fireEvent.click(within(form).getByText('general-purpose-24c'));
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    expect(await within(form).findByText('Choose a capacity pool.')).toBeInTheDocument();
    expect(mutate).not.toHaveBeenCalled();
    fireEvent.click(within(form).getByRole('combobox'));
    expect((await screen.findAllByRole('option')).map((o) => o.textContent)).toEqual(['central-metal · 3', 'edge-metal · 1']);
  });

  it('says pools are unknown when capacity cannot be read', async () => {
    mockCapacities({ error: new ApiError(403, 'forbidden') });
    mockClasses({ data: [] });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /Create class/ }));
    const form = await screen.findByTestId('fleet-plugin-create-class-form');
    expect(within(form).getByText('Capacity pools are unknown: capacity could not be read.')).toBeInTheDocument();
    expect(within(form).queryByRole('combobox')).toBeNull();
  });

  it('requires at least one profile', async () => {
    mockClasses({ data: [] });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /Create class/ }));
    const form = await screen.findByTestId('fleet-plugin-create-class-form');
    fireEvent.change(form.querySelector('input[name="name"]') as HTMLInputElement, {
      target: { value: 'c' },
    });
    choosePool(form, 'central-metal · 3');
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    expect(await within(form).findByText('Choose at least one profile.')).toBeInTheDocument();
    expect(mutate).not.toHaveBeenCalled();
  });

  it('deletes a class once its name is typed', async () => {
    mockClasses({ data: fixtureClasses.list().map(toMachineClass) });
    renderPage();
    const form = await confirmDelete('standard-metal');
    expect(within(form).getByText(/Consumers can no longer deploy from it/)).toBeInTheDocument();
    await waitFor(() => expect(remove).toHaveBeenCalled());
    expect(remove.mock.calls[0][0]).toBe('standard-metal');
  });
});

describe('toRawMachineClass', () => {
  it('offers named profiles only, with no console access', () => {
    expect(
      toRawMachineClass({
        name: 'c',
        allowedProfiles: ['a', 'b'],
        capacity: 'default/pool',
        powerControl: false,
      })
    ).toEqual({
      metadata: { name: 'c' },
      spec: {
        profilePolicy: {
          allowedProfiles: [{ name: 'a' }, { name: 'b' }],
          customProfiles: { enabled: false },
        },
        access: { consoleModes: [], powerControl: false },
        capacity: {
          machineCapacities: {
            namespace: 'default',
            selector: { matchLabels: { 'fleet.datumapis.com/capacity-pool': 'pool' } },
          },
        },
      },
    });
  });
});
