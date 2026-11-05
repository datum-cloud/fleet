import ClaimDetail from './claim-detail';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_CLAIMS } from '../lib/fixtures';
import { toClaimDetail } from '../types';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual<typeof api>('../lib/api');
  return { ...actual, useMachineClaim: vi.fn() };
});

const useClaimMock = vi.mocked(api.useMachineClaim);

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
});
