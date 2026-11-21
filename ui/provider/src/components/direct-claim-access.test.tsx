import { cleanup, render, screen } from '@testing-library/react';
import { createMemoryRouter, Outlet, RouterProvider } from 'react-router';
import { afterEach, expect, it, vi } from 'vitest';
import DeviceClaimDetail from '../pages/device-claim-detail';
import { fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords } from '../lib/fixtures';
import { inventoryResources } from '../inventory';

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
it.each(['Facility', 'Rack', 'PowerFeed'] as const)('direct claim route hides denied %s reference with real query and session boundary', async kind => {
  const claim = structuredClone(fixturePhysicalClaims[0]);
  claim.metadata.namespace = 'default';
  claim.spec.placement = kind === 'Facility' ? { scope: 'Facility', facilityRef: { name: 'secret-denied-reference' } } : { scope: 'RackMembership', rackRef: { name: 'secret-denied-reference' } };
  if (kind === 'PowerFeed') claim.spec.placement = { scope: 'RackMembership', rackRef: { name: 'readable-rack' } };
  const device = structuredClone(fixturePhysicalDevices[0]);
  device.metadata.namespace = 'default';
  device.spec.claimRef.name = claim.metadata.name;
  const cable = structuredClone(fixturePhysicalRecords.find(record => record.kind === 'Cable')!);
  cable.metadata.namespace = 'default';
  cable.spec.lanes = [{ id: 'lane', a: { deviceRef: { name: device.metadata.name }, componentID: 'port', position: 1 }, b: { powerFeedRef: { name: 'secret-denied-reference' }, componentID: 'outlet', position: 1 } }];
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    expect(init.method).toBe('GET');
    const status = url.includes(`/${inventoryResources[kind]}?`) ? 403 : 200;
    const items = url.includes('/deviceclaims?') ? [claim] : url.includes('/devices?') ? [device] : url.includes('/cables?') && kind === 'PowerFeed' ? [cable] : [];
    return { ok: status === 200, status, json: async () => ({ data: { items } }) };
  }));
  const router = createMemoryRouter([{ id: 'layouts/milo.layout', path: '/', loader: () => ({ user: { metadata: { uid: 'reviewer' } } }), element: <Outlet />, children: [{ path: 'plugins/:slug/inventory/:namespace/:claim', element: <DeviceClaimDetail /> }] }], { initialEntries: [`/plugins/test/inventory/default/${claim.metadata.name}`] });
  render(<RouterProvider router={router} />);
  await screen.findByTestId('fleet-plugin-claim');
  expect(screen.queryAllByText(/secret-denied-reference/)).toEqual([]);
  router.dispose();
});
