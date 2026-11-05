import { StatusBadge, type BadgeType } from './status-badge';
import type { MachineClass } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import type { ColumnDef } from '@tanstack/react-table';

type ClassColumn = ColumnDef<DataTableFeatures, MachineClass, unknown>;

function Muted({ children }: { children: React.ReactNode }) {
  return <span className="text-muted-foreground">{children}</span>;
}

// Fleet's class evaluator writes Accepted. No evaluator runs yet, so an
// absent condition reads as "Not evaluated" rather than implying progress.
function AcceptedBadge({ machineClass }: { machineClass: MachineClass }) {
  const { state, reason } = machineClass.accepted;
  const [type, label]: [BadgeType, string] =
    state === 'True'
      ? ['success', 'Accepted']
      : state === 'False'
        ? ['danger', 'Rejected']
        : ['muted', 'Not evaluated'];
  const title = state === 'Unknown' ? (reason ?? "Fleet hasn't evaluated this class yet.") : reason;
  return (
    <span title={title}>
      <StatusBadge type={type}>{label}</StatusBadge>
    </span>
  );
}

function matchesSearch(machineClass: MachineClass, search: string): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [machineClass.name, machineClass.capacity, ...machineClass.allowedProfiles]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(needle);
}

const columns: ClassColumn[] = [
  {
    id: 'name',
    accessorKey: 'name',
    header: 'Name',
    cell: ({ row }) => <span className="font-medium">{row.original.name}</span>,
  },
  {
    id: 'profiles',
    header: 'Profiles',
    cell: ({ row }) => {
      const { allowedProfiles, customProfiles } = row.original;
      const names = allowedProfiles.join(', ') || 'None';
      return <Muted>{customProfiles ? `${names} + custom` : names}</Muted>;
    },
  },
  {
    id: 'capacity',
    header: 'Capacity',
    cell: ({ row }) => <Muted>{row.original.source ? `from ${row.original.source}` : (row.original.capacity ?? '—')}</Muted>,
  },
  {
    id: 'power',
    header: 'Power control',
    cell: ({ row }) => <Muted>{row.original.powerControl ? 'Yes' : 'No'}</Muted>,
  },
  {
    id: 'status',
    header: 'Status',
    cell: ({ row }) => <AcceptedBadge machineClass={row.original} />,
  },
];

export function ClassTable({ classes }: { classes: MachineClass[] }) {
  return (
    <DataTable.Client
      data={classes}
      columns={columns}
      getRowId={(machineClass) => machineClass.name}
      defaultSort={[{ id: 'name', desc: false }]}
      searchFn={matchesSearch}
      className="space-y-6">
      <DataTable.Search placeholder="Search classes" className="w-full sm:max-w-xs" />
      <div className="overflow-hidden rounded-lg border" data-testid="fleet-plugin-class-table">
        <DataTable.Content
          emptyMessage={
            <p className="text-muted-foreground py-8 text-center text-sm">
              No classes match your search.
            </p>
          }
        />
      </div>
    </DataTable.Client>
  );
}
