import { StatusBadge } from './status-badge';
import { currentDevice, modelLabel, placementLabel, type InventorySlot } from '../inventory';
import { DataTable, type DataTableFeatures } from '@datum-cloud/datum-ui/data-table';
import { Text } from '@datum-cloud/datum-ui/typography';
import type { ColumnDef } from '@tanstack/react-table';
import { Link } from 'react-router';
import { inInventoryNamespace } from '../lib/inventory-namespace';

type SlotColumn = ColumnDef<DataTableFeatures, InventorySlot, unknown>;

function Muted({ children }: { children: React.ReactNode }) {
  return <Text textColor="muted">{children}</Text>;
}

function matchesSearch(slot: InventorySlot, search: string): boolean {
  const needle = search.trim().toLowerCase();
  if (!needle) return true;
  const { claim } = slot;
  return [
    claim.metadata.name,
    claim.spec.displayName,
    placementLabel(claim),
    modelLabel(claim.spec.expectedIdentity),
    claim.spec.expectedIdentity.serial,
    ...slot.devices.flatMap((d) => [d.metadata.name, d.spec.displayName, d.spec.assetTag, d.spec.providerHostRef?.id, d.spec.identity.serial, d.spec.identity.systemUUID]),
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(needle);
}

export function InventoryTable({ slots, base }: { slots: InventorySlot[]; base: string }) {
  const columns: SlotColumn[] = [
    {
      id: 'name',
      accessorFn: (slot) => slot.claim.metadata.name,
      header: 'Claim',
      cell: ({ row }) => (
        <Link
          to={`${base}/inventory/${encodeURIComponent(row.original.claim.metadata.namespace ?? '')}/${encodeURIComponent(row.original.claim.metadata.name)}`}
          className="font-medium underline-offset-4 hover:underline">
          {row.original.claim.spec.displayName ?? row.original.claim.metadata.name}
        </Link>
      ),
    },
    { id: 'placement', header: 'Placement', cell: ({ row }) => <Muted>{placementLabel(row.original.claim)}</Muted> },
    {
      id: 'model',
      header: 'Expected model',
      cell: ({ row }) => <Muted>{modelLabel(row.original.claim.spec.expectedIdentity)}</Muted>,
    },
    {
      id: 'device',
      header: 'Device',
      cell: ({ row }) => {
        const device = currentDevice(row.original);
        if (!device) return <StatusBadge type="muted">{row.original.devices.length > 1 ? 'Active binding Unknown' : 'Not registered'}</StatusBadge>;
        return <Text>{device.metadata.name}</Text>;
      },
    },
    {
      id: 'serial',
      header: 'Serial',
      cell: ({ row }) => {
        const device = currentDevice(row.original);
        return <Muted>{device?.spec.identity.serial ?? row.original.claim.spec.expectedIdentity.serial ?? '—'}</Muted>;
      },
    },
  ];

  return (
    <DataTable.Client
      data={slots.filter(slot => inInventoryNamespace(slot.claim)).map(slot => ({ ...slot, devices: slot.devices.filter(inInventoryNamespace) }))}
      columns={columns}
      getRowId={(slot) => slot.key}
      defaultSort={[{ id: 'name', desc: false }]}
      searchFn={matchesSearch}
      className="space-y-6">
      <DataTable.Search placeholder="Search claims, serials, UUIDs" className="w-full sm:max-w-xs" />
      <div className="overflow-hidden rounded-lg border" data-testid="fleet-plugin-inventory-table">
        <DataTable.Content
          emptyMessage={
            <Text as="p" textColor="muted" className="py-8 text-center">
              No claims match your search.
            </Text>
          }
        />
      </div>
    </DataTable.Client>
  );
}
