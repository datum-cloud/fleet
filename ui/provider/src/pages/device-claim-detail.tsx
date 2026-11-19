import { FleetPage } from '../components/fleet-page';
import { ErrorOrRestrictedState, LoadingState } from '../components/states';
import { StatusBadge } from '../components/status-badge';
import { PROVIDER_PROJECT, useInventory } from '../lib/api';
import { currentDevice, modelLabel, placementLabel, slotKey, type RawDevice } from '../inventory';
import type { Provenance } from '../../../shared/fleet-api';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { Text } from '@datum-cloud/datum-ui/typography';
import type { ReactNode } from 'react';
import { Link, useParams } from 'react-router';

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

function ProvenanceFields({ provenance }: { provenance: Provenance }) {
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <Field label="Recorded by">{provenance.recordedBy}</Field>
      <Field label="Recorded at">{provenance.recordedAt}</Field>
      <Field label="Method">{provenance.method}</Field>
      {provenance.reference && (
        <div className="sm:col-span-3">
          <Field label="Reference">{provenance.reference}</Field>
        </div>
      )}
    </div>
  );
}

function DeviceSection({ device, current }: { device: RawDevice; current: boolean }) {
  const { identity, bmc, interfaces = [], unmappedMACAddresses = [] } = device.spec;
  return (
    <Section title={`Device ${device.metadata.name}`} testId={`fleet-plugin-device-${device.metadata.name}`}>
      {!current && <StatusBadge type="muted">Earlier hardware for this slot</StatusBadge>}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <Field label="Model">{modelLabel(identity)}</Field>
        <Field label="Serial">{identity.serial ?? '—'}</Field>
        <Field label="System UUID">{identity.systemUUID ?? '—'}</Field>
        <Field label="BMC endpoint">{bmc?.endpoint ?? '—'}</Field>
        <Field label="Claim">{device.spec.claimRef.name}</Field>
      </div>
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
      <ProvenanceFields provenance={device.spec.provenance} />
    </Section>
  );
}

export default function DeviceClaimDetail() {
  const { slug, namespace = '', claim: claimName = '' } = useParams();
  const { data, isLoading, error, refetch } = useInventory();
  const slot = data?.slots.find((s) => s.key === slotKey(namespace, claimName));
  const current = slot && currentDevice(slot);

  return (
    <FleetPage testId="fleet-plugin-claim-detail">
      <Link to={`/plugins/${slug}/inventory`} className="text-sm underline-offset-4 hover:underline">
        ← All claims
      </Link>

      {isLoading && <LoadingState />}

      {!isLoading && error && (
        <ErrorOrRestrictedState
          error={error}
          restrictedMessage={`You don't have permission to view DeviceClaims and Devices in ${PROVIDER_PROJECT}.`}
          onRetry={() => void refetch()}
        />
      )}

      {!isLoading && !error && data && !slot && (
        <Text as="p" textColor="muted" data-testid="fleet-plugin-claim-missing">
          No DeviceClaim {claimName} in namespace {namespace}.
        </Text>
      )}

      {slot && (
        <>
          <Section title={`DeviceClaim ${slot.claim.metadata.name}`} testId="fleet-plugin-claim">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
              <Field label="Placement">{placementLabel(slot.claim)}</Field>
              <Field label="Expected model">{modelLabel(slot.claim.spec.expectedIdentity)}</Field>
              <Field label="Expected serial">{slot.claim.spec.expectedIdentity.serial ?? '—'}</Field>
              <Field label="Namespace">{slot.claim.metadata.namespace ?? '—'}</Field>
              <Field label="Bound device">
                {current ? current.metadata.name : <StatusBadge type="muted">Not registered</StatusBadge>}
              </Field>
            </div>
            {(slot.claim.spec.ports ?? []).length > 0 && (
              <div className="flex flex-col gap-1">
                <Text as="span" size="xs" textColor="muted">Expected ports</Text>
                {(slot.claim.spec.ports ?? []).map((port) => (
                  <Text key={port.name}>
                    {port.name}
                    {port.type ? ` (${port.type}${port.speedMbps ? `, ${port.speedMbps} Mb/s` : ''})` : ''}
                    {port.expectedPeer ? ` → ${port.expectedPeer.device} ${port.expectedPeer.port}` : ''}
                  </Text>
                ))}
              </div>
            )}
            <ProvenanceFields provenance={slot.claim.spec.provenance} />
          </Section>
          {[...slot.devices].reverse().map((device) => (
            <DeviceSection key={device.metadata.name} device={device} current={device === current} />
          ))}
        </>
      )}
    </FleetPage>
  );
}
