import MachineDetail from './machine-detail';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_MACHINES } from '../lib/fixtures';
import { toMachineDetail } from '../types';
import { toast } from '@datum-cloud/datum-ui/toast';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual<typeof api>('../lib/api');
  return { ...actual, useMachine: vi.fn(), useReleaseMachine: vi.fn() };
});

const useMachineMock = vi.mocked(api.useMachine);
const releaseMock = vi.fn<(name: string) => Promise<void>>();
const useReleaseMock = vi.mocked(api.useReleaseMachine);

vi.mock('@datum-cloud/datum-ui/toast', () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function mockResult(result: { data?: unknown; isLoading?: boolean; error?: unknown }) {
  useMachineMock.mockReturnValue({
    data: undefined,
    isLoading: false,
    error: null,
    refetch: vi.fn(),
    ...result,
  } as never);
}

function renderPage(name = 'example') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/project/p1/services/metal-datumapis-com/${name}`]}>
        <Routes>
          <Route path="/project/:projectId/services/:serviceSlug/:machineName" element={<MachineDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  useMachineMock.mockReset();
  releaseMock.mockReset();
  useReleaseMock.mockReturnValue({ mutateAsync: releaseMock } as never);
});

describe('MachineDetail', () => {
  it('links back to the machines list', () => {
    mockResult({ isLoading: true });
    renderPage();
    expect(screen.getByRole('link', { name: 'Machines' })).toHaveAttribute(
      'href',
      '/project/p1/services/metal-datumapis-com'
    );
    expect(screen.getByTestId('fleet-plugin-loading')).toBeInTheDocument();
  });

  it('shows a not-found state on a 404', () => {
    mockResult({ error: new ApiError(404, 'not found') });
    renderPage('gone');
    expect(screen.getByTestId('fleet-plugin-machine-not-found')).toBeInTheDocument();
  });

  it('shows the restricted state on a 403', () => {
    mockResult({ error: new ApiError(403, 'forbidden') });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-restricted')).toBeInTheDocument();
  });

  it('shows the request, the allocated hardware and every step done for a ready machine', () => {
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[0]) });
    renderPage();
    expect(within(screen.getByTestId('fleet-plugin-machine-request')).getByText('standard-metal')).toBeInTheDocument();
    const hardware = screen.getByTestId('fleet-plugin-machine-hardware');
    expect(within(hardware).getByText('2 sockets / 24 cores / 48 threads · amd64')).toBeInTheDocument();
    expect(within(hardware).getByText(/^Off/)).toBeInTheDocument();
    expect(within(screen.getByTestId('fleet-plugin-machine-network')).getByText('None requested')).toBeInTheDocument();
    expect(within(screen.getByTestId('fleet-plugin-machine-overview')).getByText('Ready · powered off')).toBeInTheDocument();
    for (const type of ['Accepted', 'Bound', 'NetworkReady', 'Ready']) {
      expect(screen.getByTestId(`fleet-plugin-step-${type}`)).toHaveAttribute('data-state', 'done');
    }
    expect(within(screen.getByTestId('fleet-plugin-step-Ready')).getByText('PoweredOffHandoff')).toBeInTheDocument();
  });

  it('explains a machine waiting for capacity and shows later steps as upcoming', () => {
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[1]) });
    renderPage('build-runner');
    expect(within(screen.getByTestId('fleet-plugin-machine-overview')).getByText('Waiting for capacity')).toBeInTheDocument();
    expect(within(screen.getByTestId('fleet-plugin-machine-hardware')).getByText(/No free server matches/)).toBeInTheDocument();
    const bound = screen.getByTestId('fleet-plugin-step-Bound');
    expect(bound).toHaveAttribute('data-state', 'current');
    expect(within(bound).getByText(/allocated to another consumer/)).toBeInTheDocument();
    expect(screen.getByTestId('fleet-plugin-step-NetworkReady')).toHaveAttribute('data-state', 'upcoming');
    expect(screen.getByTestId('fleet-plugin-step-Ready')).toHaveAttribute('data-state', 'upcoming');
    expect(screen.queryByText('Unknown')).toBeNull();
  });

  it('shows when a releasing machine was deleted', () => {
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[2]) });
    renderPage('old-worker');
    expect(screen.getByText(/releasing since/)).toBeInTheDocument();
  });

  it('hides Release once the machine is already releasing', () => {
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[2]) });
    renderPage('old-worker');
    expect(screen.queryByRole('button', { name: 'Release' })).toBeNull();
  });

  it('releases only after the machine name is typed', async () => {
    releaseMock.mockResolvedValue();
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[0]) });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: 'Release' }));

    const dialog = await screen.findByRole('dialog');
    const confirm = within(dialog).getByRole('button', { name: 'Release' });
    expect(confirm).toBeDisabled();
    fireEvent.change(within(dialog).getByRole('textbox'), { target: { value: 'example' } });
    expect(confirm).toBeEnabled();
    fireEvent.click(confirm);

    expect(useReleaseMock).toHaveBeenCalledWith('p1');
    await waitFor(() => expect(releaseMock).toHaveBeenCalledWith('example'));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(toast.success).toHaveBeenCalledWith('Releasing machine example');
  });

  it('keeps the dialog open and explains a forbidden release', async () => {
    releaseMock.mockRejectedValue(new ApiError(403, 'machines "example" is forbidden'));
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[0]) });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: 'Release' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByRole('textbox'), { target: { value: 'example' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Release' }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "You don't have permission to release machines in this project.",
        { description: 'machines "example" is forbidden' }
      )
    );
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});
