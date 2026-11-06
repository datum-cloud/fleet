import MachineClassList from './machine-class-list';
import { toRawMachineClass } from '../components/create-class-dialog';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { fixtureClasses, fixtureProfiles } from '../lib/fixtures';
import { toMachineClass, toMachineProfile } from '../types';
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

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/plugins/fleet-datumapis-com/classes']}>
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
    spec: { cpu: { architecture: 'amd64', cores: { min: 24 } }, memory: { capacity: { min: '128Gi' } } },
  },
].map(toMachineProfile);

beforeEach(() => {
  mutate.mockReset();
  remove.mockReset();
  mockDelete(api.useDeleteMachineClass);
  vi.mocked(api.useMachineProfiles).mockReturnValue({ data: PROFILES } as never);
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
    expect(screen.getByRole('tab', { name: 'Classes' })).toHaveAttribute('data-state', 'active');
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
    expect(within(tr).getByText('fleet-provider / central-metal')).toBeInTheDocument();
    expect(within(tr).getByText('Accepted')).toBeInTheDocument();
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
    fireEvent.change(input('capacityPool'), { target: { value: 'central-metal' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(mutate).toHaveBeenCalled());
    expect(mutate.mock.calls[0][0]).toEqual(
      toRawMachineClass({
        name: 'standard-metal',
        allowedProfiles: ['general-purpose-24c'],
        machineNamespace: 'default',
        capacityPool: 'central-metal',
        powerControl: true,
      })
    );
  });

  it('requires at least one profile', async () => {
    mockClasses({ data: [] });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /Create class/ }));
    const form = await screen.findByTestId('fleet-plugin-create-class-form');
    fireEvent.change(form.querySelector('input[name="name"]') as HTMLInputElement, {
      target: { value: 'c' },
    });
    fireEvent.change(form.querySelector('input[name="capacityPool"]') as HTMLInputElement, {
      target: { value: 'p' },
    });
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
        machineNamespace: 'default',
        capacityPool: 'pool',
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
          machines: {
            namespace: 'default',
            selector: { matchLabels: { 'fleet.datumapis.com/capacity-pool': 'pool' } },
          },
        },
      },
    });
  });
});
