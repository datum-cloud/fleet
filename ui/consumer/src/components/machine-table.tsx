import { MachineStatus, Power } from './machine-status';
import { usePluginBase } from './machines-page';
import { ReleaseButton } from './release-dialog';
import { machineStatus, type Machine } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import { DateTime } from '@datum-cloud/datum-ui/date-time';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { Text } from '@datum-cloud/datum-ui/typography';
import type { ColumnDef } from '@tanstack/react-table';
import { useMemo } from 'react';
import { Link } from 'react-router';

type MachineColumn = ColumnDef<DataTableFeatures, Machine, unknown>;

function Muted({ children }: { children: React.ReactNode }) {
  return <Text textColor="muted">{children}</Text>;
}

function Mono({ children }: { children: React.ReactNode }) {
  return <span className="font-mono text-xs whitespace-nowrap">{children}</span>;
}

// Search matches what the row shows: the status label, not the phase behind it.
export function matchesSearch(machine: Machine, search: string): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [
    machine.name,
    machineStatus(machine).label,
    machine.machineClass,
    machine.machineProfile,
    machine.location,
    ...machine.addresses,
  ]
    .join(' ')
    .toLowerCase()
    .includes(needle);
}

// A machine's page sits directly under the plugin's mount root.
function MachineLink({ name }: { name: string }) {
  const base = usePluginBase();
  return (
    <Link
      to={`${base}/${encodeURIComponent(name)}`}
      className="truncate font-medium hover:underline"
      title={name}>
      {name}
    </Link>
  );
}

// The first address, with the rest behind "+N".
function Addresses({ addresses }: { addresses: string[] }) {
  if (addresses.length === 0) return <Muted>—</Muted>;
  const [first, ...rest] = addresses;
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      <Mono>{first}</Mono>
      {rest.length > 0 && (
        <Tooltip message={<span className="font-mono text-xs">{rest.join(', ')}</span>}>
          <span className="text-muted-foreground cursor-default text-xs" aria-label={`and ${rest.join(', ')}`}>
            +{rest.length}
          </span>
        </Tooltip>
      )}
    </span>
  );
}

const nameColumn: MachineColumn = {
  id: 'name',
  accessorKey: 'name',
  header: 'Name',
  cell: ({ row }) => <MachineLink name={row.original.name} />,
};

const statusColumn: MachineColumn = {
  id: 'status',
  header: 'Status',
  cell: ({ row }) => <MachineStatus machine={row.original} />,
};

// The profile name already says the cores and memory; the exact values pinned
// from it are in the tooltip.
const profileColumn: MachineColumn = {
  id: 'profile',
  header: 'Profile',
  cell: ({ row }) => {
    const { machineProfile, cpu, memory } = row.original;
    const pinned = [cpu, memory].filter(Boolean).join(' · ');
    return (
      <Tooltip message={pinned} hidden={!pinned}>
        <span className="cursor-default">
          <Mono>{machineProfile}</Mono>
        </span>
      </Tooltip>
    );
  },
};

const addressColumn: MachineColumn = {
  id: 'address',
  header: 'Address',
  cell: ({ row }) => <Addresses addresses={row.original.addresses} />,
};

const locationColumn: MachineColumn = {
  id: 'location',
  header: 'Location',
  cell: ({ row }) => <Muted>{row.original.location}</Muted>,
};

const powerColumn: MachineColumn = {
  id: 'power',
  header: 'Power',
  cell: ({ row }) => (
    <Muted>
      <Power machine={row.original} />
    </Muted>
  ),
};

const createdColumn: MachineColumn = {
  id: 'created',
  header: 'Created',
  cell: ({ row }) =>
    row.original.createdAt ? (
      <DateTime
        date={row.original.createdAt}
        variant="relative"
        addSuffix
        className="text-muted-foreground text-sm whitespace-nowrap"
      />
    ) : (
      <Muted>—</Muted>
    ),
};

// Address shows only when some machine has one, and Location only when the
// machines are in more than one place.
export function machineColumns(machines: Machine[]): MachineColumn[] {
  return [
    nameColumn,
    statusColumn,
    profileColumn,
    ...(machines.some((m) => m.addresses.length > 0) ? [addressColumn] : []),
    ...(new Set(machines.map((m) => m.location)).size > 1 ? [locationColumn] : []),
    powerColumn,
    createdColumn,
  ];
}

// A machine already releasing has nothing left to release.
export function MachineTable({
  machines,
  onRelease,
}: {
  machines: Machine[];
  onRelease: (machine: Machine) => void;
}) {
  const withActions = useMemo<MachineColumn[]>(
    () => [
      ...machineColumns(machines),
      {
        id: 'actions',
        header: '',
        cell: ({ row }) =>
          row.original.phase === 'Releasing' ? null : (
            <div className="flex justify-end">
              <ReleaseButton name={row.original.name} onClick={() => onRelease(row.original)} />
            </div>
          ),
      },
    ],
    [machines, onRelease]
  );

  return (
    <DataTable.Client
      data={machines}
      columns={withActions}
      getRowId={(machine) => machine.name}
      defaultSort={[{ id: 'name', desc: false }]}
      searchFn={matchesSearch}
      className="space-y-4">
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
