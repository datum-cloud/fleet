import MachineList from './machine-list';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_MACHINES } from '../lib/fixtures';
import { FIXTURE_CLASSES, FIXTURE_PROFILES } from '../lib/fixtures';
import { toCatalog, toMachine } from '../types';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual<typeof api>('../lib/api');
  return {
    ...actual,
    useMachines: vi.fn(),
    useCatalog: vi.fn(),
    useCreateMachine: vi.fn(),
    useReleaseMachine: vi.fn(),
  };
});

const useMachinesMock = vi.mocked(api.useMachines);
const releaseMock = vi.fn<(name: string) => Promise<void>>();
const createMock = vi.fn();

vi.mock('@datum-cloud/datum-ui/toast', () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

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
      <MemoryRouter initialEntries={['/project/demo-project/services/fleet']}>
        <Routes>
          <Route path="/project/:projectId/services/:serviceSlug" element={<MachineList />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  useMachinesMock.mockReset();
  releaseMock.mockReset();
  vi.mocked(api.useReleaseMachine).mockReturnValue({ mutateAsync: releaseMock } as never);
  vi.mocked(api.useCatalog).mockReturnValue({ data: toCatalog(FIXTURE_CLASSES, FIXTURE_PROFILES) } as never);
  vi.mocked(api.useCreateMachine).mockReturnValue({ mutate: createMock, isPending: false } as never);
  createMock.mockReset();
});

describe('MachineList', () => {
  it('queries machines for the routed project', () => {
    mockResult({ data: [] });
    renderPage();
    expect(useMachinesMock).toHaveBeenCalledWith('demo-project');
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

  it('shows the empty state with a starter card per profile when the project has no machines', () => {
    mockResult({ data: [] });
    renderPage();
    const empty = screen.getByTestId('fleet-plugin-machine-empty');
    expect(within(empty).getAllByTestId('fleet-plugin-profile-card')).toHaveLength(2);
    expect(within(empty).getByText('datumctl apply -f machine.yaml')).toBeInTheDocument();
    // The hero has its own Deploy button, so the header doesn't repeat it.
    expect(screen.getAllByRole('button', { name: /Deploy machine/ })).toHaveLength(1);
  });

  it('says so when no class is available to deploy from', () => {
    vi.mocked(api.useCatalog).mockReturnValue({ data: [] } as never);
    mockResult({ data: [] });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-no-offerings')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Deploy machine/ })).toBeDisabled();
  });

  it('lists the fixture machines with their statuses', () => {
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();

    const table = screen.getByTestId('fleet-plugin-machine-table');
    for (const [name, phase] of [
      ['example', 'Ready'],
      ['build-runner', 'Waiting for capacity'],
      ['old-worker', 'Releasing'],
    ]) {
      const row = within(table).getByText(name).closest('tr');
      expect(row).not.toBeNull();
      expect(within(row as HTMLElement).getByText(phase)).toBeInTheDocument();
    }
    expect(within(table).getAllByText('24 cores · amd64 · 128Gi')).toHaveLength(2);
  });

  it('says why a class can not be deployed from yet', () => {
    const [cls] = FIXTURE_CLASSES;
    const pending = { ...cls, spec: { source: { project: 'datum-cloud', name: 'central-metal' } }, status: {} };
    vi.mocked(api.useCatalog).mockReturnValue({ data: toCatalog([pending], FIXTURE_PROFILES) } as never);
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();
    const note = screen.getByTestId('fleet-plugin-offering-unavailable');
    expect(note).toHaveTextContent('is still being set up');
    expect(screen.getByRole('button', { name: /Deploy machine/ })).toBeDisabled();
  });

  it('lists the profiles available to deploy under the table', () => {
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();
    const available = screen.getByTestId('fleet-plugin-available');
    expect(within(available).getByText('general-purpose-24c-128g')).toBeInTheDocument();
  });

  it('deploys from a profile card with that profile chosen', async () => {
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();
    fireEvent.click(
      screen.getByRole('button', { name: 'Deploy general-purpose-24c-128g from standard-metal' })
    );

    const form = await screen.findByTestId('fleet-plugin-deploy-form');
    expect(within(form).getByTestId('fleet-plugin-deploy-summary')).toHaveTextContent('standard-metal');
    fireEvent.change(within(form).getByRole('textbox'), { target: { value: 'new-runner' } });
    fireEvent.click(screen.getByRole('button', { name: 'Deploy' }));

    await waitFor(() =>
      expect(createMock).toHaveBeenCalledWith(
        { name: 'new-runner', machineClass: 'standard-metal', machineProfile: 'general-purpose-24c-128g' },
        expect.anything()
      )
    );
  });

  it('links each machine to its detail page', () => {
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();
    expect(screen.getByRole('link', { name: 'build-runner' })).toHaveAttribute(
      'href',
      '/project/demo-project/services/fleet/build-runner'
    );
  });

  it('releases a machine from its row once the name is typed', async () => {
    releaseMock.mockResolvedValue();
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: 'Release example' }));

    const dialog = await screen.findByRole('dialog');
    const confirm = within(dialog).getByRole('button', { name: 'Release' });
    expect(confirm).toBeDisabled();
    fireEvent.change(within(dialog).getByRole('textbox'), { target: { value: 'example' } });
    fireEvent.click(confirm);

    expect(api.useReleaseMachine).toHaveBeenCalledWith('demo-project');
    await waitFor(() => expect(releaseMock).toHaveBeenCalledWith('example'));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('offers no release for a machine that is already releasing', () => {
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();
    expect(screen.getByRole('button', { name: 'Release build-runner' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Release old-worker' })).toBeNull();
  });
});
