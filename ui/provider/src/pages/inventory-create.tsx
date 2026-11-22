import { Link, useLocation, useNavigate, useParams } from 'react-router';
import { FleetPage } from '../components/fleet-page';
import { InventorySessionBoundary } from '../components/inventory-session-boundary';
import { InventoryAccessState } from '../components/inventory-access-state';
import { InventoryManagement } from '../components/inventory-management';
import { InventoryPageTitle, inventoryDirectoryPath, inventoryReturnSearch } from '../components/inventory-page-title';
import { useInventory } from '../lib/api';
import type { ManagedKind } from '../lib/inventory-management';

const taskKinds = new Map<string, ManagedKind>([['device', 'Device'], ['cable', 'Cable'], ['installation-plan', 'DeviceClaim']]);

export default function InventoryCreatePage() {
  const { kind: routeKind } = useParams();
  const { pathname, search } = useLocation();
  const kind = taskKinds.get(routeKind ?? '');
  const directory = inventoryReturnSearch(search, kind ?? 'Device');
  const back = `${inventoryDirectoryPath(pathname)}${directory}`;
  const title = kind ? `Create ${kind === 'DeviceClaim' ? 'installation plan' : kind.toLowerCase()}` : 'Unknown inventory task';
  const unsupportedNamespace = new URLSearchParams(search).has('namespace') && new URLSearchParams(search).get('namespace') !== 'default';
  return <FleetPage testId="fleet-plugin-inventory-create">
    <InventoryPageTitle focusTitle title={title} directory={directory} description="Create a record only. This does not verify, reserve, or provision equipment." actions={<Link to={back}>Back to inventory</Link>} />
    {!kind || unsupportedNamespace ? <p>This task only supports default-namespace Device, Cable, and installation-plan records.</p> : <InventorySessionBoundary pending={<p role="status">Checking inventory access…</p>} unavailable={status => <InventoryAccessState status={status} />}>
      <CreateContent key={kind} kind={kind} back={back} />
    </InventorySessionBoundary>}
  </FleetPage>;
}

function CreateContent({ kind, back }: { kind: ManagedKind; back: string }) {
  const { data, error, isLoading, refetch } = useInventory();
  const navigate = useNavigate();
  if (isLoading) return <p role="status">Loading inventory references…</p>;
  if (error) return <InventoryAccessState status={error.status} onRetry={() => void refetch()} />;
  if (!data) return null;
  if (Object.values(data.issues ?? {}).some(issue => issue?.status === 401)) return <InventoryAccessState status={401} />;
  return <InventoryManagement task kind={kind} onDone={() => navigate(back)} issues={data.issues}
    facilities={data.physical.filter(record => record.kind === 'Facility')}
    racks={data.physical.filter(record => record.kind === 'Rack')}
    claims={data.slots.map(slot => slot.claim)} devices={[...data.slots.flatMap(slot => slot.devices), ...data.unboundDevices]}
    feeds={data.physical.filter(record => record.kind === 'PowerFeed')}
    catalog={data.physical.filter(record => record.kind === 'Manufacturer' || record.kind === 'DeviceType')} />;
}
