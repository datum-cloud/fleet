import { usePluginBase } from './machines-page';
import { ApiError, DEFAULT_LOCATION, useCreateMachineClaim } from '../lib/api';
import type { CatalogProfile } from '../types';
import { Form } from '@datum-cloud/datum-ui/form';
import { ConformAdapter } from '@datum-cloud/datum-ui/form/adapters/conform';
import { toast } from '@datum-cloud/datum-ui/toast';
import { useNavigate, useParams } from 'react-router';
import { z } from 'zod';

const NAME_MESSAGE =
  'Lowercase letters, numbers, and hyphens only. Must start and end with a letter or number.';

export const deployFormSchema = z.object({
  name: z
    .string({ error: 'Name is required.' })
    .max(63, 'At most 63 characters.')
    .regex(/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/, NAME_MESSAGE),
});

export interface DeployTarget {
  className: string;
  profile: CatalogProfile;
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono break-all">{value}</span>
    </div>
  );
}

// Deploying creates a MachineClaim. It doesn't reserve a machine: the claim
// starts Pending and Fleet binds one when capacity allows.
export function DeployDialog({
  target,
  onOpenChange,
}: {
  target: DeployTarget | null;
  onOpenChange: (open: boolean) => void;
}) {
  const { projectId } = useParams<{ projectId: string }>();
  const base = usePluginBase();
  const navigate = useNavigate();
  const mutation = useCreateMachineClaim(projectId);

  const handleSubmit = ({ name }: z.infer<typeof deployFormSchema>) => {
    if (!target) return;
    mutation.mutate(
      { name, machineClass: target.className, machineProfile: target.profile.name },
      {
        onSuccess: () => {
          toast.success(`Requested machine ${name}`);
          onOpenChange(false);
          navigate(base);
        },
        // A 403 is the common case (no consumer role); keep the API's
        // wording as the detail rather than the headline.
        onError: (error) => {
          if (error instanceof ApiError && error.status === 403) {
            toast.error("You don't have permission to deploy machines in this project.", {
              description: error.message,
            });
          } else {
            toast.error(error.message);
          }
        },
      }
    );
  };

  return (
    <ConformAdapter>
      <Form.Dialog
        open={!!target}
        onOpenChange={onOpenChange}
        title="Deploy a machine"
        description="Fleet allocates a machine matching this profile and hands it over powered off."
        schema={deployFormSchema}
        onSubmit={handleSubmit}
        loading={mutation.isPending}
        submitText="Deploy"
        submitTextLoading="Deploying..."
        className="w-full focus:ring-0 focus:outline-none sm:max-w-xl">
        <div
          className="divide-border space-y-0 divide-y *:px-5 *:py-5 [&>*:first-child]:pt-0 [&>*:last-child]:pb-0"
          data-testid="fleet-plugin-deploy-form">
          {target && (
            <div className="flex flex-col gap-1">
              <Row label="Class" value={target.className} />
              <Row label="Profile" value={target.profile.name} />
              <Row label="CPU" value={target.profile.cpu} />
              <Row label="Memory" value={target.profile.memory} />
              <Row label="GPUs" value={target.profile.gpus ?? 'None'} />
              <Row label="Location" value={DEFAULT_LOCATION} />
            </div>
          )}
          <Form.Field
            name="name"
            label="Name"
            description="Identifies the machine in this project. It can't be changed later."
            required>
            <Form.Input autoFocus placeholder="e.g. build-runner" />
          </Form.Field>
        </div>
      </Form.Dialog>
    </ConformAdapter>
  );
}
