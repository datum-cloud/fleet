import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { PhysicalInventory } from './physical-inventory';
import { toInventory, placementLabel } from '../inventory';
import { fixtureDeviceClaims } from '../lib/fixtures';

describe('physical inventory', () => {
  it('browses manual facilities, racks, cables and feeds without a source report', () => {
    const provenance = fixtureDeviceClaims[0].spec.provenance;
    const inventory = toInventory([], [], [
      { kind: 'Facility', metadata: { name: 'premises', namespace: 'default' }, spec: { displayName: 'Manual premises', provenance } },
      { kind: 'Rack', metadata: { name: 'rack', namespace: 'default' }, spec: { displayName: 'Manual rack', facilityRef: { name: 'premises' }, provenance } },
      { kind: 'PowerFeed', metadata: { name: 'feed', namespace: 'default' }, spec: { displayName: 'Manual feed', facilityRef: { name: 'premises' }, rackRef: { name: 'rack' }, provenance } },
      { kind: 'Cable', metadata: { name: 'cable', namespace: 'default' }, spec: { displayName: 'Manual cable', medium: 'Power', lanes: [{ id: 'power', a: { powerFeedRef: { name: 'feed' }, componentID: 'outlet', position: 1 }, b: { deviceRef: { name: 'server' }, componentID: 'psu', position: 1 } }], provenance } },
    ]);
    render(<PhysicalInventory inventory={inventory} />);
    for (const kind of ['Facility', 'Rack', 'PowerFeed', 'Cable']) {
      expect(screen.getByText(new RegExp(`^${kind}: Manual`))).toBeInTheDocument();
    }
    expect(screen.getByText(/feed\/outlet:1 ↔ server\/psu:1/)).toBeInTheDocument();
    expect(screen.getByText(/Usable budget and redundancy: Unknown/)).toBeInTheDocument();
    expect(screen.getAllByText(/Verification: Unknown/)).toHaveLength(4);
    expect(screen.queryByText(/classified records/)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /apply|sync|import|delete/i })).not.toBeInTheDocument();
  });

  it('keeps partial placement and free space unknown', () => {
    const claim = { ...fixtureDeviceClaims[0], spec: { ...fixtureDeviceClaims[0].spec, placement: { scope: 'RackMembership' as const, rackRef: { name: 'rack-example' } } } };
    expect(placementLabel(claim)).toContain('no reserved units');
    const inventory = toInventory([claim], [], [{ kind: 'Rack', metadata: { name: 'rack-example', namespace: 'default' }, spec: { displayName: 'Rack example', facilityRef: { name: 'example' }, provenance: claim.spec.provenance } }]);
    render(<PhysicalInventory inventory={inventory} />);
    expect(screen.getByText(/Free space: Unknown/)).toBeInTheDocument();
    expect(screen.queryByText(/1 registered assets/)).not.toBeInTheDocument();
    expect(screen.getByText(/0 registered assets; occupancy unverified/)).toBeInTheDocument();
  });

  it('reads reconciliation locally and rejects malformed reports', async () => {
    render(<PhysicalInventory inventory={toInventory([], [])} />);
    expect(screen.getByText(/NetBox and import reports are not required/)).toBeInTheDocument();
    const input = screen.getByLabelText(/Optional: inspect a private review report/);
    fireEvent.change(input, { target: { files: [{ size: 100, text: async () => JSON.stringify({ version: 1, records: [{ source: 'dcim/devices/1', classification: 'unresolved', reason: 'Review premises' }] }) }] } });
    await waitFor(() => expect(screen.getByText(/dcim\/devices\/1: unresolved/)).toBeInTheDocument());
    expect(screen.getByText(/Incomplete or Unknown/)).toBeInTheDocument();
    fireEvent.change(input, { target: { files: [{ size: 100, text: async () => '{"version":1,"records":[null]}' }] } });
    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument());
    expect(screen.queryByText(/dcim\/devices\/1: unresolved/)).not.toBeInTheDocument();
  });
});
