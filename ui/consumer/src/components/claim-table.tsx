import { StatusBadge, type BadgeType } from './status-badge';
import type { ClaimPhase, MachineClaim } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import type { ColumnDef } from '@tanstack/react-table';

type ClaimColumn = ColumnDef<DataTableFeatures, MachineClaim, unknown>;

const PHASE_BADGE: Record<ClaimPhase, { type: BadgeType; pending: boolean }> = {
  Ready: { type: 'success', pending: false },
  Bound: { type: 'warning', pending: true },
  Pending: { type: 'muted', pending: true },
  Releasing: { type: 'warning', pending: true },
  Rejected: { type: 'danger', pending: false },
};

function Muted({ children }: { children: React.ReactNode }) {
  return <span className="text-muted-foreground">{children}</span>;
}

function matchesSearch(claim: MachineClaim, search: string): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [claim.name, claim.phase, claim.machineClass, claim.machineProfile, claim.location]
    .join(' ')
    .toLowerCase()
    .includes(needle);
}

const columns: ClaimColumn[] = [
  {
    id: 'name',
    accessorKey: 'name',
    header: 'Name',
    cell: ({ row }) => <span className="font-medium">{row.original.name}</span>,
  },
  {
    id: 'status',
    header: 'Status',
    cell: ({ row }) => (
      <span title={row.original.phaseReason}>
        <StatusBadge {...PHASE_BADGE[row.original.phase]}>{row.original.phase}</StatusBadge>
      </span>
    ),
  },
  { id: 'class', header: 'Class', cell: ({ row }) => <Muted>{row.original.machineClass}</Muted> },
  { id: 'profile', header: 'Profile', cell: ({ row }) => <Muted>{row.original.machineProfile}</Muted> },
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
  { id: 'cpu', header: 'CPU', cell: ({ row }) => <Muted>{row.original.cpu ?? '—'}</Muted> },
  { id: 'memory', header: 'Memory', cell: ({ row }) => <Muted>{row.original.memory ?? '—'}</Muted> },
];

export function ClaimTable({ claims }: { claims: MachineClaim[] }) {
  return (
    <DataTable.Client
      data={claims}
      columns={columns}
      getRowId={(claim) => claim.name}
      defaultSort={[{ id: 'name', desc: false }]}
      searchFn={matchesSearch}
      className="space-y-6">
      <DataTable.Search placeholder="Search machines" className="w-full sm:max-w-xs" />
      <div className="overflow-hidden rounded-lg border" data-testid="fleet-plugin-claim-table">
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
