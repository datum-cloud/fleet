import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { PhysicalInventory } from './physical-inventory';
import { toInventory, placementLabel } from '../inventory';
import { fixtureDeviceClaims } from '../lib/fixtures';

describe('physical inventory', () => {
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
    const input = screen.getByLabelText(/Inspect a private import report/);
    fireEvent.change(input, { target: { files: [{ size: 100, text: async () => JSON.stringify({ version: 1, records: [{ source: 'dcim/devices/1', classification: 'unresolved', reason: 'Review premises' }] }) }] } });
    await waitFor(() => expect(screen.getByText(/dcim\/devices\/1: unresolved/)).toBeInTheDocument());
    expect(screen.getByText(/Incomplete or Unknown/)).toBeInTheDocument();
    fireEvent.change(input, { target: { files: [{ size: 100, text: async () => '{"version":1,"records":[null]}' }] } });
    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument());
    expect(screen.queryByText(/dcim\/devices\/1: unresolved/)).not.toBeInTheDocument();
  });
});
