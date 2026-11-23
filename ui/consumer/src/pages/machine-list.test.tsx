import MachineList from './machine-list';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_MACHINES } from '../lib/fixtures';
import { FIXTURE_CLASSES, FIXTURE_PROFILES } from '../lib/fixtures';
import { matchesSearch } from '../components/machine-table';
import { toCatalog, toMachine } from '../types';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { toast } from '@datum-cloud/datum-ui/toast';
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

  it('shows a retryable error on other failures, without the API text', () => {
    mockResult({ error: new ApiError(500, 'Request failed (500): /apis/metal.datumapis.com/v1alpha1/x') });
    renderPage();
    const error = screen.getByTestId('fleet-plugin-error');
    expect(error).toHaveTextContent("Couldn't load machines");
    expect(document.body.innerHTML).not.toContain('/apis/');
    expect(within(error).getByRole('button', { name: 'Retry' })).toBeInTheDocument();
  });

  it('shows the empty state with a starter card per profile when the project has no machines', () => {
    mockResult({ data: [] });
    renderPage();
    const empty = screen.getByTestId('fleet-plugin-machine-empty');
    expect(within(empty).getAllByTestId('fleet-plugin-profile-card')).toHaveLength(2);
    expect(empty).not.toHaveTextContent(/datumctl|Bound|coming soon|Use Compute|24 cores · 128Gi/);
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
      ['web-1', 'Preparing'],
    ]) {
      const row = within(table).getByText(name).closest('tr');
      expect(row).not.toBeNull();
      expect(within(row as HTMLElement).getByText(phase)).toBeInTheDocument();
    }
    // Every fixture has a creation time, so no Created cell is blank.
    expect(within(table).getAllByText(/ ago$/)).toHaveLength(4);
  });

  const headers = () => screen.getAllByRole('columnheader').map((h) => h.textContent);

  it('shows Address only when some machine has one, with the rest behind +N', () => {
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    const { unmount } = renderPage();
    expect(headers()).toEqual(['Name', 'Status', 'Profile', 'Address', 'Power', 'Created', '']);
    const row = within(screen.getByTestId('fleet-plugin-machine-table')).getByText('web-1').closest('tr') as HTMLElement;
    expect(within(row).getByText('192.0.2.10')).toBeInTheDocument();
    expect(within(row).getByLabelText('and 2001:db8::10')).toHaveTextContent('+1');
    unmount();

    mockResult({ data: FIXTURE_MACHINES.slice(0, 3).map(toMachine) });
    renderPage();
    expect(headers()).not.toContain('Address');
  });

  it('shows Location only when machines are in more than one place', () => {
    const machines = FIXTURE_MACHINES.map(toMachine);
    mockResult({ data: [...machines.slice(0, 3), { ...machines[3], location: 'us-east-1' }] });
    renderPage();
    expect(headers()).toContain('Location');
    expect(screen.getByText('us-east-1')).toBeInTheDocument();
  });

  it('shows requested power in amber only when it differs', () => {
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();
    const table = screen.getByTestId('fleet-plugin-machine-table');
    const powers = within(table).getAllByTestId('fleet-plugin-power');
    expect(powers.filter((p) => p.querySelector('.fleet-warn')).map((p) => p.getAttribute('aria-label'))).toEqual([
      'Off, requested On',
    ]);
  });

  it('searches the status labels shown, not the phases behind them', () => {
    const machines = FIXTURE_MACHINES.map(toMachine);
    const found = (q: string) => machines.filter((m) => matchesSearch(m, q)).map((m) => m.name);
    expect(found('waiting')).toEqual(['build-runner']);
    expect(found('Preparing')).toEqual(['web-1']);
    expect(found('bound')).toEqual([]);
    expect(found('192.0.2')).toEqual(['web-1']);
  });

  it('uses the same Release icon as the detail page', () => {
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();
    expect(screen.getByRole('button', { name: 'Release example' }).querySelector('svg')).toHaveClass('lucide-trash-2');
  });

  it('says why a class can not be deployed from yet', () => {
    const [cls] = FIXTURE_CLASSES;
    const pending = { ...cls, spec: { source: { project: 'datum-cloud', name: 'central-metal' } }, status: {} };
    vi.mocked(api.useCatalog).mockReturnValue({ data: toCatalog([pending], FIXTURE_PROFILES) } as never);
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();
    const card = screen.getByTestId('fleet-plugin-offering-unavailable');
    expect(card).toHaveTextContent('Setting up');
    expect(card).toHaveAttribute('aria-disabled', 'true');
    expect(card).toHaveAccessibleName("standard-metal isn't ready to deploy from yet.");
    expect(screen.getByRole('button', { name: /Deploy machine/ })).toBeDisabled();
  });

  it('never shows a class condition message or the provider project', () => {
    const failed = {
      metadata: { name: 'broken-metal' },
      spec: { source: { project: 'provider-project', name: 'broken-metal' } },
      status: { conditions: [{ type: 'SourceResolved', status: 'False' as const, reason: 'NotFound', message: 'Not published to tenant-b.' }] },
    };
    vi.mocked(api.useCatalog).mockReturnValue({ data: toCatalog([...FIXTURE_CLASSES, failed], FIXTURE_PROFILES) } as never);
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();
    const available = screen.getByTestId('fleet-plugin-available');
    expect(within(available).getAllByTestId('fleet-plugin-offering-unavailable')).toHaveLength(2);
    expect(available).not.toHaveTextContent(/Profiles from|resolve/);
    expect(document.body.innerHTML).not.toMatch(/tenant-b|provider-project|datum-cloud|from datum/);
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
    const summary = within(form).getByTestId('fleet-plugin-deploy-summary');
    expect(summary).toHaveTextContent('24 cores128Giamd64');
    expect(summary).not.toHaveTextContent(/standard-metal|us-central-1|None/);
    fireEvent.change(within(form).getByRole('textbox'), { target: { value: 'new-runner' } });
    fireEvent.click(screen.getByRole('button', { name: 'Deploy' }));

    await waitFor(() =>
      expect(createMock).toHaveBeenCalledWith(
        { name: 'new-runner', machineClass: 'standard-metal', machineProfile: 'general-purpose-24c-128g' },
        expect.anything()
      )
    );
  });

  it('reports a failed deploy in its own words, never the API text', async () => {
    mockResult({ data: FIXTURE_MACHINES.map(toMachine) });
    renderPage();
    for (const [error, said] of [
      [new ApiError(403, 'machines.metal.datumapis.com is forbidden: User "u" cannot create'), "You don't have permission to deploy machines in this project."],
      [new ApiError(409, 'machines.metal.datumapis.com "new-runner" already exists'), 'A machine named new-runner already exists.'],
      [new ApiError(500, 'Request failed (500)'), "Couldn't deploy the machine."],
    ] as const) {
      createMock.mockImplementationOnce((_input, { onError }) => onError(error));
      vi.mocked(toast.error).mockClear();
      fireEvent.click(screen.getByRole('button', { name: 'Deploy general-purpose-24c-128g from standard-metal' }));
      const form = await screen.findByTestId('fleet-plugin-deploy-form');
      fireEvent.change(within(form).getByRole('textbox'), { target: { value: 'new-runner' } });
      fireEvent.click(screen.getByRole('button', { name: 'Deploy' }));
      await waitFor(() => expect(toast.error).toHaveBeenCalledWith(said));
      fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' });
      await waitFor(() => expect(screen.queryByTestId('fleet-plugin-deploy-form')).toBeNull());
    }
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
    expect(dialog).toHaveTextContent("You lose the server and the data on it. This can't be undone.");
    expect(dialog).not.toHaveTextContent(/pool/);
    expect(within(dialog).getByRole('textbox')).toHaveAttribute('placeholder', 'example');
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
