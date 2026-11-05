import { usePluginBase } from './machines-page';
import { StatusBadge, type BadgeType } from './status-badge';
import type { ClaimPhase, MachineClaim } from '../types';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { Text } from '@datum-cloud/datum-ui/typography';
import type { ColumnDef } from '@tanstack/react-table';
import { Link } from 'react-router';

type ClaimColumn = ColumnDef<DataTableFeatures, MachineClaim, unknown>;

const PHASE_BADGE: Record<ClaimPhase, { type: BadgeType; pending: boolean }> = {
  Ready: { type: 'success', pending: false },
  Bound: { type: 'warning', pending: true },
  Pending: { type: 'muted', pending: true },
  Releasing: { type: 'warning', pending: true },
  Rejected: { type: 'danger', pending: false },
};

export function PhaseBadge({ claim }: { claim: Pick<MachineClaim, 'phase' | 'phaseReason'> }) {
  return (
    <Tooltip message={claim.phaseReason} hidden={!claim.phaseReason}>
      <span><StatusBadge {...PHASE_BADGE[claim.phase]}>{claim.phase}</StatusBadge></span>
    </Tooltip>
  );
}

function Muted({ children }: { children: React.ReactNode }) {
  return <Text textColor="muted">{children}</Text>;
}

function matchesSearch(claim: MachineClaim, search: string): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  return [claim.name, claim.phase, claim.machineClass, claim.machineProfile, claim.location]
    .join(' ')
    .toLowerCase()
    .includes(needle);
}

// A claim's page sits directly under the plugin's mount root.
function ClaimLink({ name }: { name: string }) {
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

const columns: ClaimColumn[] = [
  {
    id: 'name',
    accessorKey: 'name',
    header: 'Name',
    cell: ({ row }) => <ClaimLink name={row.original.name} />,
  },
  {
    id: 'status',
    header: 'Status',
    cell: ({ row }) => <PhaseBadge claim={row.original} />,
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
            <Text as="p" textColor="muted" className="py-8 text-center">
              No machines match your search.
            </Text>
          }
        />
      </div>
    </DataTable.Client>
  );
}
