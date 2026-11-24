import MachineDetail from './machine-detail';
import * as api from '../lib/api';
import { ApiError } from '../lib/api';
import { FIXTURE_MACHINES } from '../lib/fixtures';
import { toMachineDetail, type RawMachine } from '../types';
import { toast } from '@datum-cloud/datum-ui/toast';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
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

  it('shows the request, the hardware and every step done for a ready machine', () => {
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[0]) });
    renderPage();
    const request = screen.getByTestId('fleet-plugin-machine-request');
    expect(within(request).getByText('standard-metal')).toBeInTheDocument();
    const hardware = screen.getByTestId('fleet-plugin-machine-hardware');
    expect(within(hardware).getByText('2 sockets / 24 cores / 48 threads · amd64')).toBeInTheDocument();
    expect(within(screen.getByTestId('fleet-plugin-machine-network')).getByText('None requested')).toBeInTheDocument();
    expect(within(screen.getByTestId('fleet-plugin-machine-overview')).getByTestId('fleet-plugin-machine-status')).toHaveTextContent(
      'Ready · powered off'
    );
    for (const type of ['Accepted', 'Bound', 'NetworkReady', 'Ready']) {
      expect(screen.getByTestId(`fleet-plugin-step-${type}`)).toHaveAttribute('data-state', 'done');
    }
    expect(within(screen.getByTestId('fleet-plugin-step-Ready')).getByText('Handed over')).toBeInTheDocument();
    expect(within(screen.getByTestId('fleet-plugin-step-Bound')).getByText('Server assigned')).toBeInTheDocument();
  });

  it('states power once, in the status chip', () => {
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[0]) });
    renderPage();
    expect(screen.getAllByText(/powered|^Power$|^Off$/)).toHaveLength(1);
    expect(screen.queryByText(/reported/)).toBeNull();
    expect(screen.queryByTestId('fleet-plugin-power')).not.toHaveAttribute('aria-label');
  });

  it('shows requested power in amber only when it differs from what was reported', () => {
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[3]) });
    renderPage('web-1');
    const power = screen.getByTestId('fleet-plugin-power');
    expect(power).toHaveAttribute('aria-label', 'off, requested on');
    expect(within(power).getByText('→ on')).toHaveClass('fleet-warn');
  });

  it('puts addresses first in the network card and marks an interface that is not ready', () => {
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[3]) });
    renderPage('web-1');
    const primary = screen.getByTestId('fleet-plugin-interface-primary');
    expect(within(primary).getByText('192.0.2.10')).toBeInTheDocument();
    expect(within(primary).getByText('production')).toBeInTheDocument();
    expect(within(primary).queryByRole('img', { name: 'Not ready' })).toBeNull();
    const storage = screen.getByTestId('fleet-plugin-interface-storage');
    expect(within(storage).getByRole('img', { name: 'Not ready' })).toBeInTheDocument();
    expect(within(storage).getByText('No address')).toBeInTheDocument();
    expect(screen.queryByText(/default\//)).toBeNull();
  });

  it('marks an accepted profile that differs from the request', () => {
    const raw = FIXTURE_MACHINES[0];
    mockResult({
      data: toMachineDetail({ ...raw, status: { ...raw.status, acceptedProfileRef: { name: 'other-profile' } } }),
    });
    renderPage();
    const hardware = screen.getByTestId('fleet-plugin-machine-hardware');
    expect(within(hardware).getByText('other-profile')).toHaveClass('fleet-warn');
    expect(within(hardware).queryByText(/Accepted/)).toBeNull();
  });

  it('says waiting once in the hardware card and shows later steps as upcoming', () => {
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[1]) });
    renderPage('build-runner');
    expect(within(screen.getByTestId('fleet-plugin-machine-overview')).getByText('Waiting for capacity')).toBeInTheDocument();
    expect(within(screen.getByTestId('fleet-plugin-machine-hardware')).getByText('Waiting')).toBeInTheDocument();
    const bound = screen.getByTestId('fleet-plugin-step-Bound');
    expect(bound).toHaveAttribute('data-state', 'current');
    expect(within(bound).getByText('Waiting for capacity')).toBeInTheDocument();
    for (const type of ['NetworkReady', 'Ready']) {
      const step = screen.getByTestId(`fleet-plugin-step-${type}`);
      expect(step).toHaveAttribute('data-state', 'upcoming');
      expect(step.textContent).toBe(step.querySelector('.text-sm')?.textContent);
    }
    expect(screen.queryByText('Unknown')).toBeNull();
  });

  it('shows no internal wording', () => {
    for (const [raw, name] of [
      [FIXTURE_MACHINES[0], 'example'],
      [FIXTURE_MACHINES[1], 'build-runner'],
      [FIXTURE_MACHINES[3], 'web-1'],
    ] as const) {
      mockResult({ data: toMachineDetail(raw) });
      const { unmount } = renderPage(name);
      const text = document.body.textContent ?? '';
      for (const word of ['Bound', 'Accepted profile', 'allocation', 'binding', 'bind ', 'Installation', 'conditions', 'RequestResolved', 'Bare-metal machine']) {
        expect(text).not.toContain(word);
      }
      unmount();
    }
  });

  it('shows the creation time', () => {
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[0]) });
    renderPage();
    expect(screen.getByText(/^Created/)).toBeInTheDocument();
  });

  it('uses the same Release icon as the list row', () => {
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[0]) });
    renderPage();
    expect(screen.getByRole('button', { name: 'Release' }).querySelector('svg')).toHaveClass('lucide-trash-2');
  });

  it('shows when a releasing machine was deleted', () => {
    mockResult({ data: toMachineDetail(FIXTURE_MACHINES[2]) });
    renderPage('old-worker');
    expect(screen.getByText(/^Releasing since/)).toBeInTheDocument();
    expect(screen.queryByText(/^Created/)).toBeNull();
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
        "You don't have permission to release machines in this project."
      )
    );
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});

// Condition messages are operator text and can describe other tenants'
// capacity, so none may reach the page, its attributes or any tooltip.
describe('MachineDetail condition text', () => {
  const MESSAGES = [
    'The only synthetic machine is allocated to another consumer.',
    'Accepted by admission for tenant-b.',
    'Private custody contains the exclusive binding.',
    'Interface storage is waiting for an address.',
    'Held for tenant-c.',
  ];
  const leaky: RawMachine = {
    ...FIXTURE_MACHINES[3],
    status: {
      ...FIXTURE_MACHINES[3].status,
      conditions: [
        { type: 'Accepted', status: 'True', reason: 'UnmappedAdmission', message: MESSAGES[1] },
        { type: 'Bound', status: 'True', reason: 'ExclusiveBinding', message: MESSAGES[2] },
        // A reason that is itself message text must not reach a tooltip either.
        { type: 'NetworkReady', status: 'False', reason: MESSAGES[4], message: MESSAGES[3] },
        { type: 'Ready', status: 'False', reason: 'InsufficientCapacity', message: MESSAGES[0] },
        { type: 'RequestResolved', status: 'True', reason: 'Resolved', message: MESSAGES[0] },
      ],
    },
  };

  // Radix renders tooltip content only while open, and one at a time: focus
  // each trigger in turn and collect the page's HTML with it open.
  async function withEveryTooltip(): Promise<string[]> {
    const pages: string[] = [];
    for (const trigger of document.querySelectorAll('[data-slot="tooltip-trigger"]')) {
      await act(async () => {
        fireEvent.focus(trigger);
      });
      if (document.querySelector('[data-slot="tooltip-content"]')) pages.push(document.documentElement.outerHTML);
      await act(async () => {
        fireEvent.blur(trigger);
      });
    }
    return pages;
  }

  function expectNoMessages(html = document.documentElement.outerHTML) {
    for (const message of [...MESSAGES, ...MESSAGES.map((m) => m.replaceAll('"', '&quot;'))]) {
      expect(html).not.toContain(message);
    }
  }

  for (const [label, raw, name] of [
    ['waiting fixture', FIXTURE_MACHINES[1], 'build-runner'],
    ['messages on every condition', leaky, 'web-1'],
  ] as const) {
    it(`renders no raw message for ${label}, including tooltips`, async () => {
      mockResult({ data: toMachineDetail(raw) });
      renderPage(name);
      expectNoMessages();
      const opened = await withEveryTooltip();
      expect(opened.length).toBeGreaterThan(3);
      opened.forEach((html) => expectNoMessages(html));
    });
  }

  it('offers an unmapped token reason only as a tooltip', async () => {
    mockResult({ data: toMachineDetail(leaky) });
    renderPage('web-1');
    const accepted = screen.getByTestId('fleet-plugin-step-Accepted');
    expect(within(accepted).getByText('Done')).toBeInTheDocument();
    expect(accepted.textContent).not.toContain('UnmappedAdmission');
    const opened = await withEveryTooltip();
    expect(opened.some((html) => html.includes('UnmappedAdmission'))).toBe(true);
  });
});
