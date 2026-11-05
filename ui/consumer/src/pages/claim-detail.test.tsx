import ClaimDetail from './claim-detail';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_CLAIMS } from '../lib/fixtures';
import { toClaimDetail } from '../types';
import { toast } from '@datum-cloud/datum-ui/toast';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual<typeof api>('../lib/api');
  return { ...actual, useMachineClaim: vi.fn(), useReleaseMachineClaim: vi.fn() };
});

const useClaimMock = vi.mocked(api.useMachineClaim);
const releaseMock = vi.fn<(name: string) => Promise<void>>();
const useReleaseMock = vi.mocked(api.useReleaseMachineClaim);

vi.mock('@datum-cloud/datum-ui/toast', () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function mockResult(result: { data?: unknown; isLoading?: boolean; error?: unknown }) {
  useClaimMock.mockReturnValue({
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
      <MemoryRouter initialEntries={[`/project/p1/services/fleet-datumapis-com/${name}`]}>
        <Routes>
          <Route path="/project/:projectId/services/:serviceSlug/:claimName" element={<ClaimDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  useClaimMock.mockReset();
  releaseMock.mockReset();
  useReleaseMock.mockReturnValue({ mutateAsync: releaseMock } as never);
});

describe('ClaimDetail', () => {
  it('links back to the claims list', () => {
    mockResult({ isLoading: true });
    renderPage();
    expect(screen.getByRole('link', { name: 'Machines' })).toHaveAttribute(
      'href',
      '/project/p1/services/fleet-datumapis-com'
    );
    expect(screen.getByTestId('fleet-plugin-loading')).toBeInTheDocument();
  });

  it('shows a not-found state on a 404', () => {
    mockResult({ error: new ApiError(404, 'not found') });
    renderPage('gone');
    expect(screen.getByTestId('fleet-plugin-claim-not-found')).toBeInTheDocument();
  });

  it('shows the restricted state on a 403', () => {
    mockResult({ error: new ApiError(403, 'forbidden') });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-restricted')).toBeInTheDocument();
  });

  it('shows the request, the allocated machine and every condition for a ready claim', () => {
    mockResult({ data: toClaimDetail(FIXTURE_CLAIMS[0]) });
    renderPage();
    expect(within(screen.getByTestId('fleet-plugin-claim-request')).getByText('standard-metal')).toBeInTheDocument();
    const machine = screen.getByTestId('fleet-plugin-claim-machine');
    expect(within(machine).getByText('2 sockets / 24 cores / 48 threads · amd64')).toBeInTheDocument();
    expect(within(machine).getByText('Off')).toBeInTheDocument();
    expect(within(screen.getByTestId('fleet-plugin-condition-Ready')).getByText('PoweredOffHandoff')).toBeInTheDocument();
    expect(screen.queryByTestId('fleet-plugin-claim-network')).toBeNull();
  });

  it('explains a claim waiting for capacity', () => {
    mockResult({ data: toClaimDetail(FIXTURE_CLAIMS[1]) });
    renderPage('build-runner');
    expect(within(screen.getByTestId('fleet-plugin-claim-machine')).getByText(/No machine is allocated yet/)).toBeInTheDocument();
    const bound = screen.getByTestId('fleet-plugin-condition-Bound');
    expect(within(bound).getByText('InsufficientCapacity')).toBeInTheDocument();
    expect(within(bound).getByText(/allocated to another consumer/)).toBeInTheDocument();
  });

  it('shows when a releasing claim was deleted', () => {
    mockResult({ data: toClaimDetail(FIXTURE_CLAIMS[2]) });
    renderPage('old-worker');
    expect(within(screen.getByTestId('fleet-plugin-claim-request')).getByText('Releasing since')).toBeInTheDocument();
  });

  it('hides Release once the claim is already releasing', () => {
    mockResult({ data: toClaimDetail(FIXTURE_CLAIMS[2]) });
    renderPage('old-worker');
    expect(screen.queryByTestId('fleet-plugin-claim-release')).toBeNull();
  });

  it('releases only after the claim name is typed', async () => {
    releaseMock.mockResolvedValue();
    mockResult({ data: toClaimDetail(FIXTURE_CLAIMS[0]) });
    renderPage();
    fireEvent.click(within(screen.getByTestId('fleet-plugin-claim-release')).getByRole('button', { name: 'Release' }));

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
    releaseMock.mockRejectedValue(new ApiError(403, 'machineclaims "example" is forbidden'));
    mockResult({ data: toClaimDetail(FIXTURE_CLAIMS[0]) });
    renderPage();
    fireEvent.click(within(screen.getByTestId('fleet-plugin-claim-release')).getByRole('button', { name: 'Release' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByRole('textbox'), { target: { value: 'example' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Release' }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "You don't have permission to release machines in this project.",
        { description: 'machineclaims "example" is forbidden' }
      )
    );
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});
