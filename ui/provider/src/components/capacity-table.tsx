import { DeleteButton } from './delete-dialog';
import { SEARCH_MIN_ROWS } from './fleet-page';
import { inventoryHref } from './inventory-location';
import { INVENTORY_NAMESPACE } from '../lib/inventory-namespace';
import { capacityState, type CapacityState, type ConditionSummary, type MachineCapacity } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { Text } from '@datum-cloud/datum-ui/typography';
import type { ColumnDef } from '@tanstack/react-table';
import { useMemo } from 'react';
import { Link, useParams } from 'react-router';

type CapacityColumn = ColumnDef<DataTableFeatures, MachineCapacity, unknown>;

function Muted({ children }: { children: React.ReactNode }) {
  return <Text textColor="muted">{children}</Text>;
}

const STATE_LABEL: Record<CapacityState, string> = {
  Available: 'Available', Allocated: 'Allocated', Disabled: 'Disabled', Unhealthy: 'Unhealthy', Unavailable: 'Unavailable', Unknown: '?',
};

// Red is a fault, amber is "check this"; ordinary states carry no colour.
const STATE_ATTENTION: Partial<Record<CapacityState, 'conflict' | 'unavailable'>> = {
  Unhealthy: 'conflict', Unavailable: 'unavailable', Unknown: 'unavailable',
};

const condition = (type: string, summary: ConditionSummary) => `${type} ${summary.state}${summary.reason ? ` (${summary.reason})` : ''}`;

export function CapacityStateCell({ capacity }: { capacity: MachineCapacity }) {
  const state = capacityState(capacity);
  const reasons = [condition('Prepared', capacity.prepared), condition('Healthy', capacity.healthy), condition('Available', capacity.available)].join(' · ');
  const label = STATE_LABEL[state];
  return (
    <Tooltip message={reasons}>
      <span className="fleet-state" data-attention={STATE_ATTENTION[state]} aria-label={`${state === 'Unknown' ? 'Unknown' : label}: ${reasons}`}>{label}</span>
    </Tooltip>
  );
}

// The Device link opens the inventory record, which lives only in the
// inventory namespace; capacity elsewhere names its Device without a link.
function DeviceCell({ capacity }: { capacity: MachineCapacity }) {
  const { slug } = useParams();
  if (!capacity.device) return <Muted>—</Muted>;
  if (capacity.namespace !== INVENTORY_NAMESPACE) return <Muted>{capacity.device}</Muted>;
  return <Link className="fleet-link" to={`/plugins/${slug}/inventory${inventoryHref('Device', INVENTORY_NAMESPACE, capacity.device)}`}>{capacity.device}</Link>;
}

function matchesSearch(capacity: MachineCapacity, search: string): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [capacity.name, capacity.device, capacity.pool, capacity.allocationPolicy, STATE_LABEL[capacityState(capacity)]]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(needle);
}

// Name and State lead so they stay in view when a phone scrolls the table.
const columns: CapacityColumn[] = [
  {
    id: 'name',
    accessorKey: 'name',
    header: 'Name',
    cell: ({ row }) => <Text weight="medium">{row.original.name}</Text>,
  },
  { id: 'state', header: 'State', cell: ({ row }) => <CapacityStateCell capacity={row.original} /> },
  { id: 'device', header: 'Device', cell: ({ row }) => <DeviceCell capacity={row.original} /> },
  { id: 'pool', header: 'Pool', cell: ({ row }) => <Muted>{row.original.pool ?? '—'}</Muted> },
  {
    id: 'allocationPolicy',
    header: 'Allocation',
    cell: ({ row }) => <Muted>{row.original.allocationPolicy}</Muted>,
  },
  { id: 'cpu', header: 'CPU', cell: ({ row }) => <Muted>{row.original.cpu ?? '—'}</Muted> },
  { id: 'memory', header: 'Memory', cell: ({ row }) => <Muted>{row.original.memory ?? '—'}</Muted> },
];

export function CapacityTable({
  capacities,
  onDelete,
}: {
  capacities: MachineCapacity[];
  onDelete: (capacity: MachineCapacity) => void;
}) {
  const withActions = useMemo<CapacityColumn[]>(
    () => [
      ...columns,
      {
        id: 'actions',
        header: '',
        cell: ({ row }) => (
          <div className="flex justify-end">
            {row.original.registrationProtected ? <Muted>Retained registration</Muted> : <DeleteButton name={row.original.name} onClick={() => onDelete(row.original)} />}
          </div>
        ),
      },
    ],
    [onDelete]
  );

  return (
    <DataTable.Client
      data={capacities}
      columns={withActions}
      getRowId={(capacity) => `${capacity.namespace ?? ''}/${capacity.name}`}
      defaultSort={[{ id: 'name', desc: false }]}
      searchFn={matchesSearch}
      className="space-y-6">
      {capacities.length >= SEARCH_MIN_ROWS && <DataTable.Search placeholder="Search machine capacity" className="w-full sm:max-w-xs" />}
      <div className="fleet-table overflow-hidden rounded-lg border" data-testid="fleet-plugin-capacity-table">
        <DataTable.Content
          emptyMessage={
            <Text as="p" textColor="muted" className="py-8 text-center">
              Nothing matches your search.
            </Text>
          }
        />
      </div>
    </DataTable.Client>
  );
}
