import { FleetPage } from '../components/fleet-page';
import { PhysicalInventory } from '../components/physical-inventory';
import { LoadingState } from '../components/states';
import { InventoryAccessState } from '../components/inventory-access-state';
import { useInventory } from '../lib/api';
import { InventorySessionBoundary } from '../components/inventory-session-boundary';

export default function DeviceInventoryList() {
  return <InventorySessionBoundary><InventoryListContent /></InventorySessionBoundary>;
}

function InventoryListContent() {
  const { data, isLoading, error, refetch } = useInventory();
  return <FleetPage testId="fleet-plugin-inventory-list">
    {isLoading && <LoadingState />}
    {!isLoading && error && <InventoryAccessState status={error.status} onRetry={() => void refetch()} />}
    {!isLoading && !error && data && <PhysicalInventory inventory={data} onRetry={() => void refetch()} />}
  </FleetPage>;
}
