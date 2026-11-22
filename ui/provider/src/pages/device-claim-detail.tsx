import { FleetPage } from '../components/fleet-page';
import { LoadingState } from '../components/states';
import { InventoryAccessState } from '../components/inventory-access-state';
import { InventorySessionBoundary } from '../components/inventory-session-boundary';
import { StatusBadge } from '../components/status-badge';
import { useInventory } from '../lib/api';
import { currentDevice, endpointLabel, legacyPeerLabel, modelLabel, placementLabel, slotKey, type Inventory, type RawDevice } from '../inventory';
import { devicePlacement, reportedPlacementLabel } from '../lib/device-placement';
import type { ObjectMeta } from '../../../shared/fleet-api';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { Text } from '@datum-cloud/datum-ui/typography';
import type { ReactNode } from 'react';
import { Link, useParams } from 'react-router';
import { INVENTORY_NAMESPACE } from '../lib/inventory-namespace';
import { redactInventory } from '../lib/inventory-read-access';

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <Text as="span" size="xs" textColor="muted">{label}</Text>
      <Text as="span">{children}</Text>
    </div>
  );
}

function Section({ title, testId, children }: { title: string; testId: string; children: ReactNode }) {
  return (
    <Card data-testid={testId}>
      <CardContent className="flex flex-col gap-4">
        <Text as="p" weight="semibold">{title}</Text>
        {children}
      </CardContent>
    </Card>
  );
}

function CreatedField({ metadata }: { metadata: ObjectMeta }) {
  return <Field label="Created">{metadata.creationTimestamp ?? 'Not recorded'}</Field>;
}

function DeviceSection({ device, current, inventory }: { device: RawDevice; current: boolean; inventory: Inventory }) {
  const { identity, bmc, interfaces = [], unmappedMACAddresses = [] } = device.spec;
  const placement = devicePlacement(device, inventory);
  return (
    <Section title={`Device ${device.metadata.name}`} testId={`fleet-plugin-device-${device.metadata.name}`}>
      {!current && <StatusBadge type="muted">Historical binding; active placement Unknown</StatusBadge>}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <Field label="Model">{modelLabel(identity)}</Field>
        <Field label="Display name">{device.spec.displayName ?? device.metadata.name}</Field>
        <Field label="Location / Reported placement">{placement ? reportedPlacementLabel(placement, inventory) : 'Unknown'} · not physically verified</Field>
        <Field label="Model dimensions (not occupied units)">{device.spec.modelShape?.heightUnits ?? 'Unknown'} U</Field>
        <Field label="Serial">{identity.serial ?? '—'}</Field>
        <Field label="System UUID">{identity.systemUUID ?? '—'}</Field>
        {identity.unidentified && <Field label="Hardware identity">No serial or system UUID recorded · {identity.unidentifiedReason} · cannot back machine capacity</Field>}
        <Field label="BMC endpoint">{bmc?.endpoint ?? '—'}</Field>
        <Field label="Claim">{device.spec.claimRef.name}</Field>
        <Field label="Asset tag">{device.spec.assetTag ?? 'Unknown'}</Field>
        <Field label="Provider host ID">{device.spec.providerHostRef ? `${device.spec.providerHostRef.system}/${device.spec.providerHostRef.id}` : 'Unknown'}</Field>
      </div>
      {device.spec.components?.map((component) => <Text key={component.id}>{component.name} ({component.type}) · {component.id} · connector {component.connector ?? 'Unknown'}</Text>)}
      {device.spec.internalMappings?.map((mapping) => <Text key={`${mapping.from.componentID}:${mapping.from.position}`}>Internal path: {mapping.from.componentID}:{mapping.from.position} → {mapping.to.componentID}:{mapping.to.position}</Text>)}
      {device.spec.serviceAssociations?.map((association) => <Text key={`${association.componentID}/${association.serviceRef.id}`}>Demarcation {association.componentID} → {association.serviceRef.system}/{association.serviceRef.type}/{association.serviceRef.id}</Text>)}
      {interfaces.length > 0 && (
        <div className="flex flex-col gap-1">
          <Text as="span" size="xs" textColor="muted">Interfaces</Text>
          {interfaces.map((iface) => (
            <Text key={iface.name}>
              {iface.name}: {iface.macAddress ?? 'MAC unknown'}
              {iface.peer ? ` → ${iface.peer.device} ${iface.peer.port}` : ''}
            </Text>
          ))}
        </div>
      )}
      <div className="flex flex-col gap-1" data-testid="fleet-plugin-unmapped-macs">
        <Text as="span" size="xs" textColor="muted">Unmapped MAC addresses</Text>
        {unmappedMACAddresses.length === 0 ? (
          <Text textColor="muted">None</Text>
        ) : (
          unmappedMACAddresses.map((mac) => <Text key={mac}>{mac}</Text>)
        )}
      </div>
      <CreatedField metadata={device.metadata} />
    </Section>
  );
}

export default function DeviceClaimDetail() {
  return <InventorySessionBoundary><ClaimDetailContent /></InventorySessionBoundary>;
}

function ClaimDetailContent() {
  const { slug, namespace = '', claim: claimName = '' } = useParams();
  const { data: source, isLoading, error, refetch } = useInventory();
  const data = source && redactInventory(source, {});
  const accessIssue = error?.status ?? data?.issues?.DeviceClaim?.status;
  const slot = namespace === INVENTORY_NAMESPACE && !error && !isLoading && accessIssue === undefined ? data?.slots.find((s) => s.key === slotKey(namespace, claimName)) : undefined;
  const current = slot && !data?.issues?.Device && currentDevice(slot);

  return (
    <FleetPage testId="fleet-plugin-claim-detail">
      <Link to={`/plugins/${slug}/inventory`} className="text-sm underline-offset-4 hover:underline">
        ← All claims
      </Link>

      {isLoading && <LoadingState />}

      {!isLoading && (error || accessIssue !== undefined) && <InventoryAccessState status={accessIssue ?? 0} onRetry={() => void refetch()} />}

      {!isLoading && !error && accessIssue === undefined && data && !slot && (
        <Text as="p" textColor="muted" data-testid="fleet-plugin-claim-missing">
          {namespace !== INVENTORY_NAMESPACE ? 'This inventory UI only supports the default namespace.' : `No DeviceClaim ${claimName} in default inventory.`}
        </Text>
      )}

      {slot && (
        <>
          <Section title={`DeviceClaim ${slot.claim.metadata.name}`} testId="fleet-plugin-claim">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
              <Field label="Claim placement (intent / history)">{placementLabel(slot.claim, data?.issues)}</Field>
              <Field label="Expected model">{modelLabel(slot.claim.spec.expectedIdentity)}</Field>
              <Field label="Expected serial">{slot.claim.spec.expectedIdentity.serial ?? '—'}</Field>
              <Field label="Bound device">
                {data?.issues?.Device ? 'Device details unavailable' : current ? current.metadata.name : <StatusBadge type="muted">{slot.devices.length > 1 ? 'Multiple records; active binding Unknown' : 'Not registered'}</StatusBadge>}
              </Field>
            </div>
            {(slot.claim.spec.ports ?? []).length > 0 && (
              <div className="flex flex-col gap-1">
                <Text as="span" size="xs" textColor="muted">Expected ports</Text>
                {(slot.claim.spec.ports ?? []).map((port) => (
                  <Text key={port.name}>
                    {port.name}
                    {port.type ? ` (${port.type}${port.speedMbps ? `, ${port.speedMbps} Mb/s` : ''})` : ''}
                    {port.expectedPeer ? ` → ${legacyPeerLabel(port.expectedPeer, data?.issues, ' ')}` : ''}
                  </Text>
                ))}
              </div>
            )}
            <CreatedField metadata={slot.claim.metadata} />
          </Section>
          {data?.issues?.Device && <InventoryAccessState status={data.issues.Device.status} />}
          {(!data?.issues?.Device ? [...slot.devices].reverse() : []).map((device) => (
            <DeviceSection key={device.metadata.name} device={device} current={device === current} inventory={data!} />
          ))}
          <Section title="Related cable records" testId="fleet-plugin-related-cables">
            {data?.issues?.Cable ? <InventoryAccessState status={data.issues.Cable.status} /> : !data?.issues?.Device && data?.physical.filter((record) => record.kind === 'Cable' && record.metadata.namespace === slot.claim.metadata.namespace && record.spec.lanes?.some((lane) => slot.devices.some((device) => lane.a.deviceRef?.name === device.metadata.name || lane.b.deviceRef?.name === device.metadata.name))).map((cable) => <div key={cable.metadata.name}><Text>{cable.spec.displayName} · verification Unknown</Text>{cable.spec.lanes?.map((lane) => <Text key={lane.id}>{endpointLabel(lane.a, data?.issues)} ↔ {endpointLabel(lane.b, data?.issues)}</Text>)}</div>)}
            <Text>No displayed path proves an uncabled or free termination.</Text>
          </Section>
        </>
      )}
    </FleetPage>
  );
}
