import Catalog from './catalog';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_CLASSES, FIXTURE_PROFILES } from '../lib/fixtures';
import { toCatalog } from '../types';
import { toast } from '@datum-cloud/datum-ui/toast';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual<typeof api>('../lib/api');
  return { ...actual, useCatalog: vi.fn(), useCreateMachineClaim: vi.fn() };
});

const useCatalogMock = vi.mocked(api.useCatalog);
const mutate = vi.fn();

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
          <Route
            path="/project/:projectId/services/:serviceSlug"
            element={<p data-testid="claims-page">claims</p>}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  useCatalogMock.mockReset();
  mutate.mockReset();
  vi.mocked(api.useCreateMachineClaim).mockReturnValue({ mutate, isPending: false } as never);
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

describe('Deploy', () => {
  it('only offers Deploy on published profiles of available classes', () => {
    mockResult({ data: toCatalog(FIXTURE_CLASSES, FIXTURE_PROFILES) });
    renderPage();
    expect(screen.getAllByRole('button', { name: 'Deploy' })).toHaveLength(2);
  });

  it('creates a claim for the chosen profile and returns to the claims list', async () => {
    mutate.mockImplementation((_input, { onSuccess }) => onSuccess());
    mockResult({ data: toCatalog(FIXTURE_CLASSES, FIXTURE_PROFILES) });
    renderPage();
    const [, standard] = screen.getAllByTestId('fleet-plugin-offering');
    const card = within(standard).getAllByTestId('fleet-plugin-profile-card')[1];
    fireEvent.click(within(card).getByRole('button', { name: 'Deploy' }));

    const form = await screen.findByTestId('fleet-plugin-deploy-form');
    expect(within(form).getByText('us-central-1')).toBeInTheDocument();
    fireEvent.change(form.querySelector('input[name="name"]') as HTMLInputElement, {
      target: { value: 'trainer-1' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Deploy' }));

    await waitFor(() => expect(mutate).toHaveBeenCalled());
    expect(mutate.mock.calls[0][0]).toEqual({
      name: 'trainer-1',
      machineClass: 'standard-metal',
      machineProfile: 'accelerated-32c-256g',
    });
    expect(await screen.findByTestId('claims-page')).toBeInTheDocument();
  });

  it('explains a 403 instead of headlining the raw API error', async () => {
    const toastError = vi.spyOn(toast, 'error');
    mutate.mockImplementation((_input, { onError }) =>
      onError(new ApiError(403, 'machineclaims.fleet.datumapis.com is forbidden'))
    );
    mockResult({ data: toCatalog(FIXTURE_CLASSES, FIXTURE_PROFILES) });
    renderPage();
    fireEvent.click(screen.getAllByRole('button', { name: 'Deploy' })[0]);
    const form = await screen.findByTestId('fleet-plugin-deploy-form');
    fireEvent.change(form.querySelector('input[name="name"]') as HTMLInputElement, {
      target: { value: 'm1' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Deploy' }));
    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith(
        "You don't have permission to deploy machines in this project.",
        { description: 'machineclaims.fleet.datumapis.com is forbidden' }
      )
    );
  });

  it('rejects an invalid name without creating anything', async () => {
    mockResult({ data: toCatalog(FIXTURE_CLASSES, FIXTURE_PROFILES) });
    renderPage();
    fireEvent.click(screen.getAllByRole('button', { name: 'Deploy' })[0]);
    const form = await screen.findByTestId('fleet-plugin-deploy-form');
    fireEvent.change(form.querySelector('input[name="name"]') as HTMLInputElement, {
      target: { value: 'Not_Valid' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Deploy' }));
    expect(await screen.findByText(/Lowercase letters/)).toBeInTheDocument();
    expect(mutate).not.toHaveBeenCalled();
  });
});

describe('toRawMachineClaim', () => {
  it('requests the profile in us-central-1, powered off, in the default namespace', () => {
    expect(
      api.toRawMachineClaim({ name: 'm', machineClass: 'standard-metal', machineProfile: 'p' })
    ).toEqual({
      metadata: { name: 'm', namespace: 'default' },
      spec: {
        machineClassRef: { name: 'standard-metal' },
        machineProfileRef: { name: 'p' },
        locationRef: { name: 'us-central-1' },
        powerState: 'Off',
      },
    });
  });
});
