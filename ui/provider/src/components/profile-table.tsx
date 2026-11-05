import { DeleteButton } from './delete-dialog';
import type { MachineProfile } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import { Text } from '@datum-cloud/datum-ui/typography';
import type { ColumnDef } from '@tanstack/react-table';
import { useMemo } from 'react';

type ProfileColumn = ColumnDef<DataTableFeatures, MachineProfile, unknown>;

function Muted({ children }: { children: React.ReactNode }) {
  return <Text textColor="muted">{children}</Text>;
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
    cell: ({ row }) => <Text weight="medium">{row.original.name}</Text>,
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

export function ProfileTable({
  profiles,
  onDelete,
}: {
  profiles: MachineProfile[];
  onDelete: (profile: MachineProfile) => void;
}) {
  const withActions = useMemo<ProfileColumn[]>(
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
      data={profiles}
      columns={withActions}
      getRowId={(profile) => profile.name}
      defaultSort={[{ id: 'name', desc: false }]}
      searchFn={matchesSearch}
      className="space-y-6">
      <DataTable.Search placeholder="Search profiles" className="w-full sm:max-w-xs" />
      <div className="overflow-hidden rounded-lg border" data-testid="fleet-plugin-profile-table">
        <DataTable.Content
          emptyMessage={
            <Text as="p" textColor="muted" className="py-8 text-center">
              No profiles match your search.
            </Text>
          }
        />
      </div>
    </DataTable.Client>
  );
}
