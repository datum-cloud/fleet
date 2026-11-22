import { FleetPage } from '../components/fleet-page';
import { PhysicalInventory } from '../components/physical-inventory';
import { LoadingState } from '../components/states';
import { InventoryAccessState } from '../components/inventory-access-state';
import { useInventory } from '../lib/api';
import { InventorySessionBoundary } from '../components/inventory-session-boundary';
import { InventoryNavigation } from '../components/inventory-navigation';
import { Text } from '@datum-cloud/datum-ui/typography';
import { InventoryPendingShell } from '../components/inventory-directory-heading';

export default function DeviceInventoryList() {
  return <FleetPage testId="fleet-plugin-inventory-list">
    <InventoryNavigation />
    <InventorySessionBoundary retainPresentationData pending={<InventoryPendingShell><Text as="p" role="status" aria-busy="true">Checking inventory access…</Text></InventoryPendingShell>} unavailable={status => <InventoryPendingShell><InventoryAccessState status={status} /></InventoryPendingShell>}>
      <InventoryListContent />
    </InventorySessionBoundary>
  </FleetPage>;
}

function InventoryListContent() {
  const { data, isLoading, error, refetch } = useInventory();
  return <>
    {isLoading && <InventoryPendingShell><LoadingState /></InventoryPendingShell>}
    {!isLoading && error && <InventoryPendingShell><InventoryAccessState status={error.status} onRetry={() => void refetch()} /></InventoryPendingShell>}
    {!isLoading && !error && data && <PhysicalInventory inventory={data} showNavigation={false} onRetry={() => void refetch()} />}
  </>;
}
