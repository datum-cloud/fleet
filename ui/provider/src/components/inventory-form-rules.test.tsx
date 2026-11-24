import { selectOptions, selectedValue, setField } from '../test/field-select';
import { findEnabledButton } from '../test/enabled-button';
import { chooseEndpoint } from '../test/cable-endpoint';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, expect, it, vi } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { DevicePlacementEdit } from './device-placement-edit';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';
import type { Inventory } from '../inventory';
import type { PhysicalRecord } from '../../../shared/fleet-api';

const claim = { ...fixturePhysicalClaims[0], metadata: { ...fixturePhysicalClaims[0].metadata, uid: 'plan-uid', resourceVersion: '1' }, spec: { ...fixturePhysicalClaims[0].spec, lifecycle: 'Installation' as const } };
const device = { ...fixturePhysicalDevices[0], metadata: { ...fixturePhysicalDevices[0].metadata, uid: 'device-uid', resourceVersion: '1', managedFields: [] } };
const feed = { ...fixturePhysicalRecords[2], metadata: { ...fixturePhysicalRecords[2].metadata, uid: 'feed-uid', resourceVersion: '1' } };
const cable: PhysicalRecord = { kind: 'Cable', metadata: { name: 'synthetic-cable', namespace: 'default', uid: 'cable-uid', resourceVersion: '1' }, spec: { displayName: 'Synthetic patch', lanes: [{ id: 'lane-1', a: { deviceRef: { name: device.metadata.name }, componentID: 'eth0', position: 1 }, b: { powerFeedRef: { name: feed.metadata.name }, componentID: 'unused', position: 1 } }] } };
const response = (data: unknown, status = 200) => ({ ok: status < 400, status, json: async () => ({ data }) });
afterEach(() => vi.unstubAllGlobals());

function writes(get: (url: string) => unknown = () => claim) {
  const sent: unknown[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (url.includes('selfsubjectaccessreviews')) return response({ status: { allowed: true } });
    if (init.method === 'GET') return response(get(url));
    sent.push(init); return response({ metadata: { uid: 'new-uid' } });
  }));
  return sent;
}
function renderForm(kind: 'Device' | 'Cable', props: { devices?: typeof device[]; issues?: Inventory['issues']; cables?: PhysicalRecord[] } = {}) {
  return render(<QueryClientProvider client={new QueryClient()}><InventoryManagement kind={kind} facilities={[]} claims={[claim]} devices={props.devices ?? [device]} feeds={[feed]} cables={props.cables} issues={props.issues} /></QueryClientProvider>);
}
const change = (label: string, value: string) => setField(screen.getByLabelText(label), value);

it('requires a serial or system UUID before submit and names the field, without a request', async () => {
  const sent = writes();
  renderForm('Device');
  fireEvent.click(await findEnabledButton({ name: 'Create device' }));
  change('Installation plan', 'plan-uid'); change('Display name', 'Synthetic record'); change('Model', 'Recorded model');
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  const summary = await screen.findByRole('alert');
  expect(summary).toHaveTextContent('Serial number: Enter a serial number or a system UUID.');
  expect(screen.getByLabelText('Serial number')).toHaveAttribute('aria-invalid', 'true');
  expect(screen.queryByText(/API could not accept/)).not.toBeInTheDocument();
  change('System UUID', 'NOT-A-UUID');
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/System UUID: Enter a lowercase UUID/));
  expect(screen.getByRole('alert')).not.toHaveTextContent('Serial number:');
  expect(sent).toEqual([]);
});

it('shows the form\'s own reference message instead of a generic API error', async () => {
  const sent = writes(() => ({ ...claim, metadata: { ...claim.metadata, resourceVersion: '2' } }));
  renderForm('Device');
  fireEvent.click(await findEnabledButton({ name: 'Create device' }));
  change('Installation plan', 'plan-uid'); change('Display name', 'Synthetic record'); change('Model', 'Recorded model'); change('Serial number', 'SYNTHETIC-NEW');
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('The installation plan changed. Review it before registration.'));
  expect(screen.queryByText(/API could not accept|already exists, changed, or has another writer/)).not.toBeInTheDocument();
  expect(sent).toEqual([]);
});

it('fills the record name from the display name until it is edited, and reports the created kind by its label', async () => {
  writes();
  renderForm('Device');
  fireEvent.click(await findEnabledButton({ name: 'Create device' }));
  // The plan comes first; the names follow it.
  expect(screen.getByLabelText('Installation plan').compareDocumentPosition(screen.getByLabelText('Display name')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  change('Display name', 'Example Server  A01/U2');
  expect(screen.getByLabelText('Record name')).toHaveValue('example-server-a01-u2');
  change('Record name', 'chosen-name'); change('Display name', 'Another name');
  expect(screen.getByLabelText('Record name')).toHaveValue('chosen-name');
  change('Installation plan', 'plan-uid'); change('Model', 'Recorded model'); change('Serial number', 'SYNTHETIC-NEW');
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  await screen.findByText('Device created.');
  expect(screen.queryByText(/DeviceClaim|record created/)).not.toBeInTheDocument();
});

it('marks installation plans that already have a device', async () => {
  writes();
  renderForm('Device', { devices: [{ ...device, spec: { ...device.spec, claimRef: { name: claim.metadata.name } } }] });
  fireEvent.click(await findEnabledButton({ name: 'Create device' }));
  const plan = selectOptions(screen.getByLabelText('Installation plan')).find(option => /has device/.test(option.text))!.element;
  expect(plan).toHaveClass('inventory-option-attention');
  expect(plan).toHaveAttribute('title', 'A device is already registered to this plan');
});

it('rejects duplicate component IDs before submit', async () => {
  const sent = writes();
  renderForm('Device');
  fireEvent.click(await findEnabledButton({ name: 'Create device' }));
  change('Installation plan', 'plan-uid'); change('Display name', 'Synthetic record'); change('Model', 'Recorded model'); change('Serial number', 'SYNTHETIC-NEW');
  for (const index of [1, 2]) {
    fireEvent.click(screen.getByRole('button', { name: 'Add component' }));
    change(`Component ${index} id`, 'nic-1'); change(`Component ${index} name`, `NIC ${index}`);
  }
  // One row per component, in a table.
  expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(3);
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Component 2 id: Component IDs must be unique.');
  expect(sent).toEqual([]);
});

it('picks a cable end by device then port, marks cabled ports in amber and rejects the same port twice', async () => {
  const sent = writes();
  renderForm('Cable', { cables: [cable] });
  fireEvent.click(await findEnabledButton({ name: 'Create cable' }));
  change('Display name', 'Synthetic record'); change('Cable medium', 'Copper');
  expect(selectOptions(screen.getByLabelText('Lane 1 end A'))).toHaveLength(0);
  change('Lane 1 end A device', `Device/${device.metadata.uid}`);
  const ports = selectOptions(screen.getByLabelText('Lane 1 end A'));
  const cabled = ports.find(option => option.text === 'Ethernet 0 · cabled')!.element;
  expect(cabled).toHaveClass('inventory-option-attention');
  expect(cabled).toHaveAttribute('title', 'Already cabled: Synthetic patch');
  expect(ports.find(option => option.text === 'Power supply 1')!.element).not.toHaveClass('inventory-option-attention');
  chooseEndpoint('Lane 1 end A', 'Ethernet 0');
  expect(screen.getByLabelText('Lane 1 end A')).toHaveAttribute('data-attention', 'cabled');
  chooseEndpoint('Lane 1 end B', 'Ethernet 0');
  expect(screen.getByLabelText('Lane 1 end B')).toHaveAttribute('data-attention', 'conflict');
  fireEvent.click(screen.getByRole('button', { name: 'Create record' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Lane 1 end B: Same port as end A.');
  fireEvent.click(screen.getByRole('button', { name: 'Add lane' }));
  expect(screen.getByLabelText('Lane 2 ID')).toHaveValue('lane-2');
  expect(sent).toEqual([]);
});

it('does not offer cable creation, and so unmarked ports, when cables are unreadable', async () => {
  writes();
  renderForm('Cable', { issues: { Cable: { status: 403 } } });
  // Without the Cable collection, an unmarked port could already be cabled; creation is unavailable and the reason is the trigger's description.
  await waitFor(() => expect(screen.getByRole('button', { name: 'Create cable' })).toHaveAccessibleDescription('Needs inventory management access'));
});

it('puts the placement editor\'s disabled reason in its tooltip, not on the page', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ status: { allowed: false } })));
  render(<QueryClientProvider client={new QueryClient()}><DevicePlacementEdit device={device} locations={[]} /></QueryClientProvider>);
  const trigger = screen.getByRole('button', { name: /Correct location|Add reported location/ });
  await waitFor(() => expect(trigger).toHaveAccessibleDescription('Needs inventory management access'));
  expect(trigger).toBeDisabled();
  expect(trigger.closest('.inventory-management-controls')).toHaveAttribute('title', 'Needs inventory management access');
  expect(screen.getByRole('status')).toBeEmptyDOMElement();
  expect(screen.queryByRole('heading', { name: 'Reported location' })).not.toBeInTheDocument();
});

it('keeps facility and rack consistent and shows its own geometry message', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => url.includes('selfsubjectaccessreviews') ? response({ status: { allowed: true } }) : response(device)));
  const facility = (name: string): PhysicalRecord => ({ kind: 'Facility', metadata: { name, namespace: 'default', uid: `${name}-uid` }, spec: { displayName: name } });
  const rack: PhysicalRecord = { kind: 'Rack', metadata: { name: 'rack-b', namespace: 'default', uid: 'rack-b-uid' }, spec: { displayName: 'Rack B', facilityRef: { name: 'site-b' } } };
  render(<QueryClientProvider client={new QueryClient()}><DevicePlacementEdit device={{ ...device, spec: { ...device.spec, reportedPlacement: undefined } }} locations={[facility('site-a'), facility('site-b'), rack]} /></QueryClientProvider>);
  fireEvent.click(await findEnabledButton({ name: 'Add reported location' }));
  expect(screen.queryByText(/Leave unknown values blank|records only/)).not.toBeInTheDocument();
  expect(screen.getByLabelText('Evidence reference')).toHaveAccessibleDescription('No credentials or secrets.');
  change('Rack', 'rack-b');
  expect(selectedValue(screen.getByLabelText('Facility'))).toBe('site-b');
  change('Facility', 'site-a');
  expect(selectedValue(screen.getByLabelText('Rack'))).toBe('');
  change('Facility', ''); change('Mount (non-U)', 'shelf');
  change('Correction reason', 'Synthetic correction'); change('Evidence reference', 'ticket/synthetic');
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Geometry requires a rack.'));
  expect(screen.getByRole('button', { name: 'Save changes' })).toBeEnabled();
});

it('uses field patterns that compile under the browser\'s unicode-sets mode', async () => {
  writes();
  renderForm('Device');
  fireEvent.click(await findEnabledButton({ name: 'Create device' }));
  // Browsers compile pattern with the v flag; an invalid pattern is silently skipped.
  for (const field of [screen.getByLabelText('Record name'), screen.getByLabelText('System UUID')]) expect(() => new RegExp(`^(?:${field.getAttribute('pattern')})$`, 'v')).not.toThrow();
});
