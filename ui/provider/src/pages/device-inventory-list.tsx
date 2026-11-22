import { FleetPage } from '../components/fleet-page';
import { PhysicalInventory } from '../components/physical-inventory';
import { LoadingState } from '../components/states';
import { InventoryAccessState } from '../components/inventory-access-state';
import { useInventory } from '../lib/api';
import { InventorySessionBoundary } from '../components/inventory-session-boundary';
import { InventoryNavigation } from '../components/inventory-navigation';
import { Text } from '@datum-cloud/datum-ui/typography';

export default function DeviceInventoryList() {
  return <FleetPage testId="fleet-plugin-inventory-list">
    <InventoryNavigation />
    <InventorySessionBoundary retainPresentationData pending={<Text as="p" role="status" aria-busy="true">Checking inventory access…</Text>}>
      <InventoryListContent />
    </InventorySessionBoundary>
  </FleetPage>;
}

function InventoryListContent() {
  const { data, isLoading, error, refetch } = useInventory();
  return <>
    {isLoading && <LoadingState />}
    {!isLoading && error && <InventoryAccessState status={error.status} onRetry={() => void refetch()} />}
    {!isLoading && !error && data && <PhysicalInventory inventory={data} showNavigation={false} onRetry={() => void refetch()} />}
  </>;
}
