import MachineProfileList from './machine-profile-list';
import { toRawMachineProfile } from '../components/create-profile-dialog';
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
    useMachineProfiles: vi.fn(),
    useCreateMachineProfile: vi.fn(),
    useMachineClasses: vi.fn(),
    useDeleteMachineProfile: vi.fn(),
  };
});

const useProfilesMock = vi.mocked(api.useMachineProfiles);
const useCreateMock = vi.mocked(api.useCreateMachineProfile);
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

function mockList(result: { data?: unknown; isLoading?: boolean; error?: unknown }) {
  useProfilesMock.mockReturnValue({
    data: undefined,
    isLoading: false,
    error: null,
    refetch: vi.fn(),
    ...result,
  } as never);
}

function mockCreate(result: { error?: unknown; isPending?: boolean } = {}) {
  useCreateMock.mockReturnValue({
    mutate,
    reset: vi.fn(),
    error: null,
    isPending: false,
    ...result,
  } as never);
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/plugins/metal-datumapis-com/profiles']}>
        <Routes>
          <Route path="/plugins/:slug/profiles" element={<MachineProfileList />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  useProfilesMock.mockReset();
  mutate.mockReset();
  mockCreate();
  remove.mockReset();
  mockDelete(api.useDeleteMachineProfile);
  vi.mocked(api.useMachineClasses).mockReturnValue({
    data: fixtureClasses.list().map(toMachineClass),
  } as never);
});

describe('MachineProfileList', () => {
  it('links the Capacity and Profiles tabs under the plugin mount', () => {
    mockList({ data: [] });
    renderPage();
    expect(screen.getByRole('tab', { name: 'Capacity' }).closest('a')).toHaveAttribute(
      'href',
      '/plugins/metal-datumapis-com'
    );
    expect(screen.getByRole('tab', { name: 'Profiles' })).toHaveAttribute('data-state', 'active');
  });

  it('shows the restricted state on a 403', () => {
    mockList({ error: new ApiError(403, 'forbidden') });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-restricted')).toBeInTheDocument();
  });

  it('shows the empty state when there are no profiles', () => {
    mockList({ data: [] });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-profile-empty')).toBeInTheDocument();
  });

  it('lists profiles with their requirements', () => {
    mockList({ data: fixtureProfiles.list().map(toMachineProfile) });
    renderPage();
    const table = screen.getByTestId('fleet-plugin-profile-table');
    const tr = within(table).getByText('general-purpose-24c-128g').closest('tr') as HTMLElement;
    expect(within(tr).getByText('≥ 24 cores · amd64')).toBeInTheDocument();
    expect(within(tr).getByText('≥ 128Gi')).toBeInTheDocument();
    expect(within(tr).getByText('None')).toBeInTheDocument();
  });

  it('creates a profile from the dialog', async () => {
    mockList({ data: [] });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /Create profile/ }));
    const form = await screen.findByTestId('fleet-plugin-create-profile-form');
    const input = (name: string) =>
      form.querySelector(`input[name="${name}"]`) as HTMLInputElement;
    fireEvent.change(input('name'), { target: { value: 'compute-32c-256g' } });
    fireEvent.change(input('minCores'), { target: { value: '32' } });
    fireEvent.change(input('minMemory'), { target: { value: '256Gi' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(mutate).toHaveBeenCalled());
    expect(mutate.mock.calls[0][0]).toEqual({
      metadata: { name: 'compute-32c-256g' },
      spec: {
        cpu: { architecture: 'amd64', cores: { min: 32 } },
        memory: { capacity: { min: '256Gi' } },
      },
    });
  });

  it('shows a failed create inside the dialog', async () => {
    mockList({ data: [] });
    mockCreate({ error: new ApiError(403, 'machineprofiles.metal.datumapis.com is forbidden') });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /Create profile/ }));
    const form = await screen.findByTestId('fleet-plugin-create-profile-form');
    expect(within(form).getByText(/is forbidden/)).toBeInTheDocument();
  });

  it('names the classes that allow a profile before deleting it', async () => {
    mockList({ data: fixtureProfiles.list().map(toMachineProfile) });
    renderPage();
    const form = await confirmDelete('general-purpose-24c-128g');
    expect(within(form).getByText(/Allowed by standard-metal/)).toBeInTheDocument();
    await waitFor(() => expect(remove).toHaveBeenCalled());
    expect(remove.mock.calls[0][0]).toBe('general-purpose-24c-128g');
  });
});

describe('toRawMachineProfile', () => {
  it('adds GPUs only when a count is given', () => {
    const base = { name: 'p', architecture: 'arm64', minCores: 8, minMemory: '32Gi' } as const;
    expect(toRawMachineProfile(base).spec.gpus).toBeUndefined();
    expect(toRawMachineProfile({ ...base, minGpus: 2, gpuVendor: 'nvidia' }).spec.gpus).toEqual({
      count: { min: 2 },
      vendor: 'nvidia',
    });
  });
});
