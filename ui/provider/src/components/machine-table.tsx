import { DeleteButton } from './delete-dialog';
import { StatusBadge, type BadgeType } from './status-badge';
import type { ConditionSummary, Machine } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { Text } from '@datum-cloud/datum-ui/typography';
import type { ColumnDef } from '@tanstack/react-table';
import { useMemo } from 'react';

type MachineColumn = ColumnDef<DataTableFeatures, Machine, unknown>;

function Muted({ children }: { children: React.ReactNode }) {
  return <Text textColor="muted">{children}</Text>;
}

// `falseType` lets Available=False read as neutral: a Disabled machine is
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

function matchesSearch(machine: Machine, search: string): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [machine.name, machine.namespace, machine.pool, machine.allocationPolicy]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(needle);
}

const columns: MachineColumn[] = [
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

export function MachineTable({
  machines,
  onDelete,
}: {
  machines: Machine[];
  onDelete: (machine: Machine) => void;
}) {
  const withActions = useMemo<MachineColumn[]>(
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
      data={machines}
      columns={withActions}
      getRowId={(machine) => `${machine.namespace ?? ''}/${machine.name}`}
      defaultSort={[{ id: 'name', desc: false }]}
      searchFn={matchesSearch}
      className="space-y-6">
      <DataTable.Search placeholder="Search machines" className="w-full sm:max-w-xs" />
      <div className="overflow-hidden rounded-lg border" data-testid="fleet-plugin-machine-table">
        <DataTable.Content
          emptyMessage={
            <Text as="p" textColor="muted" className="py-8 text-center">
              No machines match your search.
            </Text>
          }
        />
      </div>
    </DataTable.Client>
  );
}
