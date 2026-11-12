import { useCreateMachineProfile } from '../lib/api';
import type { RawMachineProfile } from '../types';
import { Alert, AlertDescription } from '@datum-cloud/datum-ui/alert';
import { Button } from '@datum-cloud/datum-ui/button';
import { Form } from '@datum-cloud/datum-ui/form';
import { ConformAdapter } from '@datum-cloud/datum-ui/form/adapters/conform';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { PlusIcon } from 'lucide-react';
import { useState } from 'react';
import { z } from 'zod';

const NAME_MESSAGE =
  'Lowercase letters, numbers, and hyphens only. Must start and end with a letter or number.';
const QUANTITY_MESSAGE = 'A size such as 128Gi or 2Ti.';

export const ARCHITECTURES = ['amd64', 'arm64'] as const;

// The minimum a profile needs: CPU and memory floors, optionally GPUs. Disks
// and interface slots can follow once a page needs them.
export const profileFormSchema = z.object({
  name: z
    .string({ error: 'Name is required.' })
    .max(63, 'At most 63 characters.')
    .regex(/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/, NAME_MESSAGE),
  architecture: z.enum(ARCHITECTURES, { error: 'Choose an architecture.' }),
  minCores: z
    .number({ error: 'Enter a whole number.' })
    .int('Enter a whole number.')
    .min(1, 'At least 1.'),
  minMemory: z
    .string({ error: 'Memory is required.' })
    .regex(/^[1-9][0-9]*(Mi|Gi|Ti)$/, QUANTITY_MESSAGE),
  minGpus: z
    .number({ error: 'Enter a whole number.' })
    .int('Enter a whole number.')
    .min(1, 'At least 1, or leave blank for none.')
    .optional(),
  gpuVendor: z.string().max(63).optional(),
});

export type ProfileFormValues = z.infer<typeof profileFormSchema>;

export function toRawMachineProfile(values: ProfileFormValues): RawMachineProfile {
  return {
    metadata: { name: values.name },
    spec: {
      cpu: { architecture: values.architecture, cores: { min: values.minCores } },
      memory: { capacity: { min: values.minMemory } },
      ...(values.minGpus !== undefined && {
        gpus: {
          count: { min: values.minGpus },
          ...(values.gpuVendor && { vendor: values.gpuVendor }),
        },
      }),
    },
  };
}

// Errors render in the dialog: staff-portal does not share its toast module,
// so a plugin's toast would never reach the host's Toaster. Width and padding
// use the classes staff-portal's own dialog-form uses; a plugin has no CSS
// build, so classes the host never uses (sm:max-w-xl) silently do nothing.
export function CreateProfileDialog() {
  const [open, setOpen] = useState(false);
  const mutation = useCreateMachineProfile();

  const setOpenAndReset = (next: boolean) => {
    if (next) mutation.reset();
    setOpen(next);
  };

  return (
    <>
      <Button
        type="primary"
        theme="solid"
        size="small"
        htmlType="button"
        className="w-full sm:w-auto"
        onClick={() => setOpenAndReset(true)}>
        <Icon icon={PlusIcon} className="size-4" />
        Create profile
      </Button>
      <ConformAdapter>
        <Form.Dialog
          open={open}
          onOpenChange={setOpenAndReset}
          title="Create a machine profile"
          description="Hardware requirements a class can offer. A profile can't be changed after it is created."
          schema={profileFormSchema}
          defaultValues={{ architecture: 'amd64' }}
          onSubmit={(values) =>
            mutation.mutate(toRawMachineProfile(values), { onSuccess: () => setOpen(false) })
          }
          loading={mutation.isPending}
          submitText="Create"
          submitTextLoading="Creating..."
          className="w-full focus:ring-0 focus:outline-none sm:max-w-lg">
          <div className="space-y-4 px-5" data-testid="fleet-plugin-create-profile-form">
            {mutation.error && (
              <Alert variant="destructive">
                <AlertDescription>{mutation.error.message}</AlertDescription>
              </Alert>
            )}
            <Form.Field name="name" label="Name" required>
              <Form.Input autoFocus placeholder="e.g. general-purpose-24c-128g" />
            </Form.Field>
            <Form.Field name="architecture" label="CPU architecture" required>
              <Form.Select>
                {ARCHITECTURES.map((arch) => (
                  <Form.SelectItem key={arch} value={arch}>
                    {arch}
                  </Form.SelectItem>
                ))}
              </Form.Select>
            </Form.Field>
            <Form.Field name="minCores" label="Minimum CPU cores" required>
              <Form.Input type="number" inputMode="numeric" placeholder="24" />
            </Form.Field>
            <Form.Field
              name="minMemory"
              label="Minimum memory"
              description="A Kubernetes quantity, such as 128Gi."
              required>
              <Form.Input placeholder="128Gi" />
            </Form.Field>
            <Form.Field
              name="minGpus"
              label="Minimum GPUs"
              description="Leave blank for a profile without GPUs.">
              <Form.Input type="number" inputMode="numeric" />
            </Form.Field>
            <Form.Field name="gpuVendor" label="GPU vendor">
              <Form.Input placeholder="e.g. nvidia" />
            </Form.Field>
          </div>
        </Form.Dialog>
      </ConformAdapter>
    </>
  );
}
