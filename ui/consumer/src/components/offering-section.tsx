import { StatusBadge } from './status-badge';
import type { CatalogOffering, CatalogProfile } from '../types';
import { Button } from '@datum-cloud/datum-ui/button';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Text } from '@datum-cloud/datum-ui/typography';
import { ArrowRightIcon, CpuIcon, LayersIcon, WarehouseIcon } from 'lucide-react';

function Spec({ label, value, tag }: { label: string; value?: string; tag?: string }) {
  return (
    <div className="flex items-center gap-2 border-b py-2 text-sm last:border-b-0">
      <Text textColor="muted">{label}</Text>
      <span className="ml-auto flex items-center gap-2">
        {tag && (
          <span className="bg-muted text-muted-foreground rounded px-1.5 py-0.5 font-mono text-xs">
            {tag}
          </span>
        )}
        {value ? (
          <Text weight="medium" className="tabular-nums">
            {value}
          </Text>
        ) : (
          <Text textColor="muted">None</Text>
        )}
      </span>
    </div>
  );
}

// summarizeProfile renders CPU as "<cores> · <architecture>"; the architecture
// reads better as a tag beside the core count.
function splitCpu(cpu: string): { cores: string; arch?: string } {
  const i = cpu.lastIndexOf(' · ');
  return i < 0 ? { cores: cpu } : { cores: cpu.slice(0, i), arch: cpu.slice(i + 3) };
}

function ProfileCard({
  profile,
  onDeploy,
}: {
  profile: CatalogProfile;
  onDeploy: (profile: CatalogProfile) => void;
}) {
  const { cores, arch } = splitCpu(profile.cpu);
  return (
    <Card data-testid="fleet-plugin-profile-card">
      <CardContent className="flex h-full flex-col gap-4">
        <div className="flex items-start gap-3">
          <span className="bg-primary/10 text-primary flex size-9 shrink-0 items-center justify-center rounded-lg">
            <Icon icon={CpuIcon} size={18} aria-hidden />
          </span>
          <Text as="p" weight="semibold" className="min-w-0 flex-1 pt-1.5 break-all">
            {profile.name}
          </Text>
          {!profile.published && <StatusBadge type="muted">Not published</StatusBadge>}
        </div>
        {profile.published && (
          <div className="flex flex-col">
            <Text size="xs" weight="medium" textColor="muted" className="tracking-wide uppercase">
              Hardware
            </Text>
            <Spec label="CPU" value={cores} tag={arch} />
            <Spec label="Memory" value={profile.memory} />
            <Spec label="GPUs" value={profile.gpus} />
          </div>
        )}
        {profile.published && (
          <Button
            type="primary"
            theme="solid"
            htmlType="button"
            className="mt-auto w-full"
            onClick={() => onDeploy(profile)}>
            Deploy
            <Icon icon={ArrowRightIcon} size={14} aria-hidden />
          </Button>
        )}
      </CardContent>
    </Card>
  );
}

// One class: its name, where it comes from, and the profiles it allows.
export function OfferingSection({
  offering,
  onDeploy,
}: {
  offering: CatalogOffering;
  onDeploy: (className: string, profile: CatalogProfile) => void;
}) {
  return (
    <section data-testid="fleet-plugin-offering" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2 border-b pb-3">
        <Icon icon={WarehouseIcon} size={16} className="text-muted-foreground" aria-hidden />
        <Text as="h2" size="base" weight="semibold">
          {offering.className}
        </Text>
        {offering.available ? (
          <StatusBadge type="success">Available</StatusBadge>
        ) : offering.failed ? (
          <StatusBadge type="danger">Unavailable</StatusBadge>
        ) : (
          <StatusBadge type="warning" pending>
            Pending
          </StatusBadge>
        )}
        {offering.source && (
          <Text size="xs" textColor="muted" className="ml-auto font-mono">
            from {offering.source}
          </Text>
        )}
      </div>

      {!offering.available && (
        <Text as="p" textColor="muted">
          {offering.unavailableReason}
        </Text>
      )}

      {offering.available && offering.profiles.length === 0 && (
        <Text as="p" textColor="muted">
          This class allows no profiles yet.
        </Text>
      )}

      {offering.available && offering.profiles.length > 0 && (
        // Inline: the host's stylesheet only carries the utilities it uses
        // itself, and an arbitrary grid template is not one of them.
        <div
          className="grid gap-4"
          style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(18rem, 1fr))' }}>
          {offering.profiles.map((profile) => (
            <ProfileCard
              key={profile.name}
              profile={profile}
              onDeploy={(p) => onDeploy(offering.className, p)}
            />
          ))}
          <div className="text-muted-foreground flex flex-col items-center justify-center gap-1.5 rounded-xl border p-5 text-center">
            <Icon icon={LayersIcon} size={18} aria-hidden />
            <Text size="sm" weight="medium" textColor="muted">
              More profiles show up here
            </Text>
            <Text size="xs" textColor="muted">
              when they're added to {offering.className}.
            </Text>
          </div>
        </div>
      )}
    </section>
  );
}
