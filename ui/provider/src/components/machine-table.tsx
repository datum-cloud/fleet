import { StatusBadge, type BadgeType } from './status-badge';
import type { ConditionSummary, Machine } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import type { ColumnDef } from '@tanstack/react-table';

type MachineColumn = ColumnDef<DataTableFeatures, Machine, unknown>;

function Muted({ children }: { children: React.ReactNode }) {
  return <span className="text-muted-foreground">{children}</span>;
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
    <span title={summary.reason}>
      <StatusBadge type={type}>{label}</StatusBadge>
    </span>
  );
}

function matchesSearch(machine: Machine, search: string): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [machine.name, machine.pool, machine.allocationPolicy]
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
    cell: ({ row }) => <span className="font-medium">{row.original.name}</span>,
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

export function MachineTable({ machines }: { machines: Machine[] }) {
  return (
    <DataTable.Client
      data={machines}
      columns={columns}
      getRowId={(machine) => `${machine.namespace ?? ''}/${machine.name}`}
      defaultSort={[{ id: 'name', desc: false }]}
      searchFn={matchesSearch}
      className="space-y-6">
      <DataTable.Search placeholder="Search machines" className="w-full sm:max-w-xs" />
      <div className="overflow-hidden rounded-lg border" data-testid="fleet-plugin-machine-table">
        <DataTable.Content
          emptyMessage={
            <p className="text-muted-foreground py-8 text-center text-sm">
              No machines match your search.
            </p>
          }
        />
      </div>
    </DataTable.Client>
  );
}
