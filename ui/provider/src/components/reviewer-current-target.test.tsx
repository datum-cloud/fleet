import { render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { expect, it } from 'vitest';
import { InventoryManagement } from './inventory-management';
import { inventoryPayload, INVENTORY_FIELD_MANAGER } from '../lib/inventory-management';

it('renders the production editor with the provenance-free contract', () => {
  const record = JSON.parse(JSON.stringify({ ...inventoryPayload('Facility', { name: 'independent', namespace: 'default', displayName: 'Independent facility', facility: '', height: '' }), metadata: { name: 'independent', namespace: 'default', uid: 'synthetic', resourceVersion: '1', managedFields: [{ manager: INVENTORY_FIELD_MANAGER }] } }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  expect(() => render(<QueryClientProvider client={client}><InventoryManagement kind="Facility" record={record} facilities={[]} /></QueryClientProvider>)).not.toThrow();
});
