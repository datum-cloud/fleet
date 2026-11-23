import { DeleteButton } from './delete-dialog';
import { SEARCH_MIN_ROWS } from './fleet-page';
import { StatusBadge, type BadgeType } from './status-badge';
import { capacityState, type MachineCapacity, type MachineClass } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { Text } from '@datum-cloud/datum-ui/typography';
import type { ColumnDef } from '@tanstack/react-table';
import { useMemo } from 'react';
import { Link, useParams } from 'react-router';

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

/** The capacity list, or "unknown" when it could not be read. */
export type ClassCapacities = MachineCapacity[] | 'unknown' | undefined;

// The pool the class selects, with "available / in pool" from live capacity,
// linked to the Capacity tab filtered to that pool. Amber when nothing in the
// pool is available, "?" when capacity cannot be read.
function PoolCell({ machineClass, capacities }: { machineClass: MachineClass; capacities: ClassCapacities }) {
  const { slug } = useParams();
  const { source, capacityNamespace: namespace, capacityPool: pool } = machineClass;
  if (source) return <Muted>from {source}</Muted>;
  if (!pool || !namespace) return <Muted>—</Muted>;
  const inPool = Array.isArray(capacities) ? capacities.filter((c) => c.namespace === namespace && c.pool === pool) : undefined;
  const available = inPool?.filter((c) => capacityState(c) === 'Available').length;
  const counts = capacities === 'unknown' ? '?' : inPool ? `${available}/${inPool.length}` : undefined;
  const title = capacities === 'unknown' ? `Capacity in ${namespace} could not be read` : inPool ? `${available} available of ${inPool.length} in pool ${pool} (${namespace})` : undefined;
  const attention = capacities === 'unknown' || available === 0 ? 'unavailable' : undefined;
  return (
    <Tooltip message={title} hidden={!title}>
      <Link className="fleet-link" to={`/plugins/${slug}?${new URLSearchParams({ namespace, pool })}`} aria-label={title ? `${pool}: ${title}` : pool}>
        {pool}{counts && <> · <span className="fleet-state" data-attention={attention}>{counts}</span></>}
      </Link>
    </Tooltip>
  );
}

function matchesSearch(machineClass: MachineClass, search: string): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [machineClass.name, machineClass.capacityPool, machineClass.source, ...machineClass.allowedProfiles]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(needle);
}

const columnsFor = (capacities: ClassCapacities): ClassColumn[] => [
  {
    id: 'name',
    accessorKey: 'name',
    header: 'Name',
    cell: ({ row }) => <Text weight="medium">{row.original.name}</Text>,
  },
  {
    id: 'capacity',
    header: 'Capacity',
    cell: ({ row }) => <PoolCell machineClass={row.original} capacities={capacities} />,
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
  capacities,
  onDelete,
}: {
  classes: MachineClass[];
  capacities: ClassCapacities;
  onDelete: (machineClass: MachineClass) => void;
}) {
  const withActions = useMemo<ClassColumn[]>(
    () => [
      ...columnsFor(capacities),
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
    [capacities, onDelete]
  );

  return (
    <DataTable.Client
      data={classes}
      columns={withActions}
      getRowId={(machineClass) => machineClass.name}
      defaultSort={[{ id: 'name', desc: false }]}
      searchFn={matchesSearch}
      className="space-y-6">
      {classes.length >= SEARCH_MIN_ROWS && <DataTable.Search placeholder="Search classes" className="w-full sm:max-w-xs" />}
      <div className="fleet-table overflow-hidden rounded-lg border" data-testid="fleet-plugin-class-table">
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
