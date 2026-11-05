import type { MachineProfile } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import type { ColumnDef } from '@tanstack/react-table';

type ProfileColumn = ColumnDef<DataTableFeatures, MachineProfile, unknown>;

function Muted({ children }: { children: React.ReactNode }) {
  return <span className="text-muted-foreground">{children}</span>;
}

function matchesSearch(profile: MachineProfile, search: string): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [profile.name, profile.cpu, profile.memory, profile.gpus]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(needle);
}

const columns: ProfileColumn[] = [
  {
    id: 'name',
    accessorKey: 'name',
    header: 'Name',
    cell: ({ row }) => <span className="font-medium">{row.original.name}</span>,
  },
  { id: 'cpu', header: 'CPU', cell: ({ row }) => <Muted>{row.original.cpu}</Muted> },
  { id: 'memory', header: 'Memory', cell: ({ row }) => <Muted>{row.original.memory}</Muted> },
  { id: 'gpus', header: 'GPUs', cell: ({ row }) => <Muted>{row.original.gpus ?? 'None'}</Muted> },
  {
    id: 'slots',
    header: 'Disks / NICs',
    cell: ({ row }) => (
      <Muted>
        {row.original.disks} / {row.original.interfaces}
      </Muted>
    ),
  },
];

export function ProfileTable({ profiles }: { profiles: MachineProfile[] }) {
  return (
    <DataTable.Client
      data={profiles}
      columns={columns}
      getRowId={(profile) => profile.name}
      defaultSort={[{ id: 'name', desc: false }]}
      searchFn={matchesSearch}
      className="space-y-6">
      <DataTable.Search placeholder="Search profiles" className="w-full sm:max-w-xs" />
      <div className="overflow-hidden rounded-lg border" data-testid="fleet-plugin-profile-table">
        <DataTable.Content
          emptyMessage={
            <p className="text-muted-foreground py-8 text-center text-sm">
              No profiles match your search.
            </p>
          }
        />
      </div>
    </DataTable.Client>
  );
}
