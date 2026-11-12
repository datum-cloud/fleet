import { DeleteButton } from './delete-dialog';
import { StatusBadge, type BadgeType } from './status-badge';
import type { ConditionSummary, MachineCapacity } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { Text } from '@datum-cloud/datum-ui/typography';
import type { ColumnDef } from '@tanstack/react-table';
import { useMemo } from 'react';

type CapacityColumn = ColumnDef<DataTableFeatures, MachineCapacity, unknown>;

function Muted({ children }: { children: React.ReactNode }) {
  return <Text textColor="muted">{children}</Text>;
}

// `falseType` lets Available=False read as neutral: Disabled capacity is
// expected to be unavailable, while an unhealthy one is a problem.
function ConditionBadge({
  summary,
  falseType = 'danger',
}: {
  summary: ConditionSummary;
  falseType?: BadgeType;
}) {
  const type: BadgeType =
    summary.state === 'True' ? 'success' : summary.state === 'False' ? falseType : 'muted';
  const label = summary.state === 'True' ? 'Yes' : summary.state === 'False' ? 'No' : 'Unknown';
  return (
    <Tooltip message={summary.reason} hidden={!summary.reason}>
      <span><StatusBadge type={type}>{label}</StatusBadge></span>
    </Tooltip>
  );
}

function matchesSearch(capacity: MachineCapacity, search: string): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [capacity.name, capacity.namespace, capacity.pool, capacity.allocationPolicy]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(needle);
}

const columns: CapacityColumn[] = [
  {
    id: 'name',
    accessorKey: 'name',
    header: 'Name',
    cell: ({ row }) => <Text weight="medium">{row.original.name}</Text>,
  },
  {
    id: 'namespace',
    header: 'Namespace',
    cell: ({ row }) => <Muted>{row.original.namespace ?? '—'}</Muted>,
  },
  { id: 'pool', header: 'Pool', cell: ({ row }) => <Muted>{row.original.pool ?? '—'}</Muted> },
  {
    id: 'allocationPolicy',
    header: 'Allocation',
    cell: ({ row }) => <Muted>{row.original.allocationPolicy}</Muted>,
  },
  { id: 'cpu', header: 'CPU', cell: ({ row }) => <Muted>{row.original.cpu ?? '—'}</Muted> },
  { id: 'memory', header: 'Memory', cell: ({ row }) => <Muted>{row.original.memory ?? '—'}</Muted> },
  {
    id: 'prepared',
    header: 'Prepared',
    cell: ({ row }) => <ConditionBadge summary={row.original.prepared} />,
  },
  {
    id: 'healthy',
    header: 'Healthy',
    cell: ({ row }) => <ConditionBadge summary={row.original.healthy} />,
  },
  {
    id: 'available',
    header: 'Available',
    cell: ({ row }) => <ConditionBadge summary={row.original.available} falseType="muted" />,
  },
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
            <DeleteButton name={row.original.name} onClick={() => onDelete(row.original)} />
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
      <DataTable.Search placeholder="Search machine capacity" className="w-full sm:max-w-xs" />
      <div className="overflow-hidden rounded-lg border" data-testid="fleet-plugin-capacity-table">
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
