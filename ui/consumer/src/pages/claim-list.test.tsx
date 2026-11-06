import ClaimList from './claim-list';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_CLAIMS } from '../lib/fixtures';
import { toMachineClaim } from '../types';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual<typeof api>('../lib/api');
  return { ...actual, useMachineClaims: vi.fn(), useReleaseMachineClaim: vi.fn() };
});

const useMachineClaimsMock = vi.mocked(api.useMachineClaims);
const releaseMock = vi.fn<(name: string) => Promise<void>>();

vi.mock('@datum-cloud/datum-ui/toast', () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function mockResult(result: { data?: unknown; isLoading?: boolean; error?: unknown }) {
  useMachineClaimsMock.mockReturnValue({
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
          <Route path="/project/:projectId/services/:serviceSlug" element={<ClaimList />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  useMachineClaimsMock.mockReset();
  releaseMock.mockReset();
  vi.mocked(api.useReleaseMachineClaim).mockReturnValue({ mutateAsync: releaseMock } as never);
});

describe('ClaimList', () => {
  it('queries claims for the routed project', () => {
    mockResult({ data: [] });
    renderPage();
    expect(useMachineClaimsMock).toHaveBeenCalledWith('demo-project');
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

  it('shows the empty state when the project has no claims', () => {
    mockResult({ data: [] });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-claim-empty')).toBeInTheDocument();
  });

  it('lists the fixture claims with their statuses', () => {
    mockResult({ data: FIXTURE_CLAIMS.map(toMachineClaim) });
    renderPage();

    const table = screen.getByTestId('fleet-plugin-claim-table');
    for (const [name, phase] of [
      ['example', 'Ready'],
      ['build-runner', 'Pending'],
      ['old-worker', 'Releasing'],
    ]) {
      const row = within(table).getByText(name).closest('tr');
      expect(row).not.toBeNull();
      expect(within(row as HTMLElement).getByText(phase)).toBeInTheDocument();
    }
    expect(within(table).getAllByText('24 cores · amd64')).toHaveLength(2);
  });

  it('links each claim to its detail page', () => {
    mockResult({ data: FIXTURE_CLAIMS.map(toMachineClaim) });
    renderPage();
    expect(screen.getByRole('link', { name: 'build-runner' })).toHaveAttribute(
      'href',
      '/project/demo-project/services/fleet/build-runner'
    );
  });

  it('releases a claim from its row once the name is typed', async () => {
    releaseMock.mockResolvedValue();
    mockResult({ data: FIXTURE_CLAIMS.map(toMachineClaim) });
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: 'Release example' }));

    const dialog = await screen.findByRole('dialog');
    const confirm = within(dialog).getByRole('button', { name: 'Release' });
    expect(confirm).toBeDisabled();
    fireEvent.change(within(dialog).getByRole('textbox'), { target: { value: 'example' } });
    fireEvent.click(confirm);

    expect(api.useReleaseMachineClaim).toHaveBeenCalledWith('demo-project');
    await waitFor(() => expect(releaseMock).toHaveBeenCalledWith('example'));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('offers no release for a claim that is already releasing', () => {
    mockResult({ data: FIXTURE_CLAIMS.map(toMachineClaim) });
    renderPage();
    expect(screen.getByRole('button', { name: 'Release build-runner' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Release old-worker' })).toBeNull();
  });
});
