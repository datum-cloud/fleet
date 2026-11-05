import { DeleteButton } from './delete-dialog';
import { StatusBadge, type BadgeType } from './status-badge';
import type { MachineClass } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { Text } from '@datum-cloud/datum-ui/typography';
import type { ColumnDef } from '@tanstack/react-table';
import { useMemo } from 'react';

type ClassColumn = ColumnDef<DataTableFeatures, MachineClass, unknown>;

function Muted({ children }: { children: React.ReactNode }) {
  return <Text textColor="muted">{children}</Text>;
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
    <Tooltip message={title} hidden={!title}>
      <span><StatusBadge type={type}>{label}</StatusBadge></span>
    </Tooltip>
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
    cell: ({ row }) => <Text weight="medium">{row.original.name}</Text>,
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

export function ClassTable({
  classes,
  onDelete,
}: {
  classes: MachineClass[];
  onDelete: (machineClass: MachineClass) => void;
}) {
  const withActions = useMemo<ClassColumn[]>(
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
      data={classes}
      columns={withActions}
      getRowId={(machineClass) => machineClass.name}
      defaultSort={[{ id: 'name', desc: false }]}
      searchFn={matchesSearch}
      className="space-y-6">
      <DataTable.Search placeholder="Search classes" className="w-full sm:max-w-xs" />
      <div className="overflow-hidden rounded-lg border" data-testid="fleet-plugin-class-table">
        <DataTable.Content
          emptyMessage={
            <Text as="p" textColor="muted" className="py-8 text-center">
              No classes match your search.
            </Text>
          }
        />
      </div>
    </DataTable.Client>
  );
}
