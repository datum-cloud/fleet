import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, it } from 'vitest';
import { PhysicalInventory } from './physical-inventory';
import { fixturePhysicalClaims } from '../lib/fixtures';
import { toInventory } from '../inventory';

afterEach(cleanup);
it.each([403, 503])('legacy expected peer must not disclose denied Device identity after %s', status => {
  const claim = structuredClone(fixturePhysicalClaims[0]);
  delete claim.spec.lifecycle;
  claim.spec.ports = [{ name: 'eth0', expectedPeer: { device: 'secret-denied-device', port: 'secret-port' } }];
  const inventory = { ...toInventory([claim], [], []), issues: { Device: { status } } };
  const params = new URLSearchParams({ kind: 'DeviceClaim', record: claim.metadata.name, namespace: claim.metadata.namespace! });
  render(<QueryClientProvider client={new QueryClient()}><MemoryRouter initialEntries={[`/?${params}`]}><PhysicalInventory inventory={inventory} /></MemoryRouter></QueryClientProvider>);
  fireEvent.click(screen.getByText('Legacy expected ports'));
  expect(screen.queryByText(/secret-denied-device/)).not.toBeInTheDocument();
});
