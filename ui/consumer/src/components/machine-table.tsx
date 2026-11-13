import { MachineStatus } from './machine-status';
import { usePluginBase } from './machines-page';
import { ReleaseButton } from './release-dialog';
import type { Machine } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import { DateTime } from '@datum-cloud/datum-ui/date-time';
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

function matchesSearch(machine: Machine, search: string): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [machine.name, machine.phase, machine.machineClass, machine.machineProfile, machine.location]
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

const columns: MachineColumn[] = [
  {
    id: 'name',
    accessorKey: 'name',
    header: 'Name',
    cell: ({ row }) => <MachineLink name={row.original.name} />,
  },
  {
    id: 'status',
    header: 'Status',
    cell: ({ row }) => <MachineStatus machine={row.original} />,
  },
  {
    id: 'profile',
    header: 'Profile',
    cell: ({ row }) => <Mono>{row.original.machineProfile}</Mono>,
  },
  {
    id: 'hardware',
    header: 'Hardware',
    // Allocated hardware, once Fleet has bound some.
    cell: ({ row }) => {
      const { cpu, memory } = row.original;
      return cpu || memory ? <Mono>{[cpu, memory].filter(Boolean).join(' · ')}</Mono> : <Muted>—</Muted>;
    },
  },
  { id: 'location', header: 'Location', cell: ({ row }) => <Muted>{row.original.location}</Muted> },
  {
    id: 'power',
    header: 'Power',
    // Show the observation; intent alone does not mean the machine is on.
    cell: ({ row }) => {
      const { powerObserved, powerIntent } = row.original;
      if (!powerObserved) return <Muted>—</Muted>;
      return (
        <Muted>
          {powerObserved}
          {powerIntent !== powerObserved ? ` → ${powerIntent}` : ''}
        </Muted>
      );
    },
  },
  {
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
  },
];

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
      ...columns,
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
    [onRelease]
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
