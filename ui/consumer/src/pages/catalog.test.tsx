import Catalog from './catalog';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_CLASSES, FIXTURE_PROFILES } from '../lib/fixtures';
import { toCatalog } from '../types';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual<typeof api>('../lib/api');
  return { ...actual, useCatalog: vi.fn() };
});

const useCatalogMock = vi.mocked(api.useCatalog);

function mockResult(result: { data?: unknown; isLoading?: boolean; error?: unknown }) {
  useCatalogMock.mockReturnValue({
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
      <MemoryRouter initialEntries={['/project/p1/services/fleet-datumapis-com/catalog']}>
        <Routes>
          <Route path="/project/:projectId/services/:serviceSlug/catalog" element={<Catalog />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  useCatalogMock.mockReset();
});

describe('Catalog', () => {
  it('activates the Catalog tab and links back to the claims list', () => {
    mockResult({ data: [] });
    renderPage();
    expect(screen.getByRole('tab', { name: 'Catalog' })).toHaveAttribute('data-state', 'active');
    expect(screen.getByRole('tab', { name: 'Machines' }).closest('a')).toHaveAttribute(
      'href',
      '/project/p1/services/fleet-datumapis-com'
    );
  });

  it('shows the restricted state on a 403', () => {
    mockResult({ error: new ApiError(403, 'forbidden') });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-restricted')).toBeInTheDocument();
  });

  it('shows the empty state when nothing is published', () => {
    mockResult({ data: [] });
    renderPage();
    expect(screen.getByTestId('fleet-plugin-catalog-empty')).toBeInTheDocument();
  });

  it('shows profile cards for available classes and a note for pending ones', () => {
    mockResult({ data: toCatalog(FIXTURE_CLASSES, FIXTURE_PROFILES) });
    renderPage();
    const [pending, standard] = screen.getAllByTestId('fleet-plugin-offering');

    expect(within(standard).getByText('from datum-cloud/standard-metal')).toBeInTheDocument();
    const cards = within(standard).getAllByTestId('fleet-plugin-profile-card');
    expect(cards).toHaveLength(2);
    expect(within(cards[1]).getByText('4 × nvidia')).toBeInTheDocument();

    expect(within(pending).getByText('high-memory-metal')).toBeInTheDocument();
    expect(within(pending).getByText(/Waiting for the provider/)).toBeInTheDocument();
    expect(within(pending).queryByTestId('fleet-plugin-profile-card')).toBeNull();
  });
});
