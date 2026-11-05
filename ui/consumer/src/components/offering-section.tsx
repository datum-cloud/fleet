import { StatusBadge } from './status-badge';
import type { CatalogOffering, CatalogProfile } from '../types';
import { Button } from '@datum-cloud/datum-ui/button';
import { Card, CardContent } from '@datum-cloud/datum-ui/card';
import { Text } from '@datum-cloud/datum-ui/typography';

function Spec({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4 text-sm">
      <Text textColor="muted">{label}</Text>
      <Text>{value}</Text>
    </div>
  );
}

function ProfileCard({
  profile,
  onDeploy,
}: {
  profile: CatalogProfile;
  onDeploy: (profile: CatalogProfile) => void;
}) {
  return (
    <Card data-testid="fleet-plugin-profile-card">
      <CardContent className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-2">
          <Text as="p" weight="semibold" className="break-all">
            {profile.name}
          </Text>
          {!profile.published && <StatusBadge type="muted">Not published</StatusBadge>}
        </div>
        {profile.published && (
          <div className="flex flex-col gap-1">
            <Spec label="CPU" value={profile.cpu} />
            <Spec label="Memory" value={profile.memory} />
            <Spec label="GPUs" value={profile.gpus ?? 'None'} />
          </div>
        )}
        {profile.published && (
          <Button
            type="primary"
            theme="solid"
            size="small"
            htmlType="button"
            className="w-fit"
            onClick={() => onDeploy(profile)}>
            Deploy
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
      <div className="flex flex-wrap items-center gap-2">
        <Text as="h2" size="base" weight="semibold">
          {offering.className}
        </Text>
        {offering.available ? (
          <StatusBadge type="success">Available</StatusBadge>
        ) : (
          <StatusBadge type="warning" pending>
            Pending
          </StatusBadge>
        )}
        {offering.source && (
          <Text textColor="muted">from {offering.source}</Text>
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
        <div className="grid gap-4 sm:grid-cols-2">
          {offering.profiles.map((profile) => (
            <ProfileCard
              key={profile.name}
              profile={profile}
              onDeploy={(p) => onDeploy(offering.className, p)}
            />
          ))}
        </div>
      )}
    </section>
  );
}
