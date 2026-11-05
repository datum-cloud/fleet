import { CAPACITY_POOL_LABEL, type MachineProfile, type RawMachineClass } from '../types';
import { useCreateMachineClass, useMachineProfiles } from '../lib/api';
import { Alert, AlertDescription } from '@datum-cloud/datum-ui/alert';
import { Button } from '@datum-cloud/datum-ui/button';
import { Checkbox } from '@datum-cloud/datum-ui/checkbox';
import { Form } from '@datum-cloud/datum-ui/form';
import { ConformAdapter } from '@datum-cloud/datum-ui/form/adapters/conform';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { PlusIcon } from 'lucide-react';
import { useState } from 'react';
import { z } from 'zod';

const NAME_PATTERN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const NAME_MESSAGE =
  'Lowercase letters, numbers, and hyphens only. Must start and end with a letter or number.';

// A class is the offering: the profiles consumers may claim and the pool of
// Machines that serves them. Custom profiles and console access stay off;
// the first implementation supports power control but no console sessions.
export const classFormSchema = z.object({
  name: z.string({ error: 'Name is required.' }).max(63, 'At most 63 characters.').regex(NAME_PATTERN, NAME_MESSAGE),
  allowedProfiles: z.array(z.string(), { error: 'Choose at least one profile.' }).min(1, 'Choose at least one profile.'),
  machineNamespace: z
    .string({ error: 'Namespace is required.' })
    .max(63, 'At most 63 characters.')
    .regex(NAME_PATTERN, NAME_MESSAGE),
  capacityPool: z
    .string({ error: 'Capacity pool is required.' })
    .max(63, 'At most 63 characters.')
    .regex(NAME_PATTERN, NAME_MESSAGE),
  powerControl: z.boolean().optional(),
});

export type ClassFormValues = z.infer<typeof classFormSchema>;

export function toRawMachineClass(values: ClassFormValues): RawMachineClass {
  return {
    metadata: { name: values.name },
    spec: {
      profilePolicy: {
        allowedProfiles: values.allowedProfiles.map((name) => ({ name })),
        customProfiles: { enabled: false },
      },
      access: { consoleModes: [], powerControl: !!values.powerControl },
      capacity: {
        machines: {
          namespace: values.machineNamespace,
          selector: { matchLabels: { [CAPACITY_POOL_LABEL]: values.capacityPool } },
        },
      },
    },
  };
}

function ProfileChoices({ profiles }: { profiles: MachineProfile[] | undefined }) {
  if (!profiles?.length) {
    return (
      <p className="text-muted-foreground text-sm">
        No profiles yet. Create one on the Profiles tab first.
      </p>
    );
  }
  // Checkboxes sharing a name submit as an array, which the schema parses.
  return (
    <div className="flex flex-col gap-2">
      {profiles.map((profile) => (
        <label key={profile.name} className="flex items-center gap-2 text-sm">
          <Checkbox name="allowedProfiles" value={profile.name} />
          <span className="font-medium">{profile.name}</span>
          <span className="text-muted-foreground">
            {profile.cpu} · {profile.memory}
          </span>
        </label>
      ))}
    </div>
  );
}

// Errors render in the dialog because staff-portal does not share its toast
// module with plugins.
export function CreateClassDialog() {
  const [open, setOpen] = useState(false);
  const mutation = useCreateMachineClass();
  const { data: profiles } = useMachineProfiles();

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
        Create class
      </Button>
      <ConformAdapter>
        <Form.Dialog
          open={open}
          onOpenChange={setOpenAndReset}
          title="Create a machine class"
          description="An offering consumers can claim from: the profiles it allows and the Machines that serve it."
          schema={classFormSchema}
          defaultValues={{ machineNamespace: 'default', powerControl: true }}
          onSubmit={(values) =>
            mutation.mutate(toRawMachineClass(values), { onSuccess: () => setOpen(false) })
          }
          loading={mutation.isPending}
          submitText="Create"
          submitTextLoading="Creating..."
          className="w-full focus:ring-0 focus:outline-none sm:max-w-lg">
          <div className="space-y-4 px-5" data-testid="fleet-plugin-create-class-form">
            {mutation.error && (
              <Alert variant="destructive">
                <AlertDescription>{mutation.error.message}</AlertDescription>
              </Alert>
            )}
            <Form.Field name="name" label="Name" required>
              <Form.Input autoFocus placeholder="e.g. standard-metal" />
            </Form.Field>
            <Form.Field name="allowedProfiles" label="Allowed profiles" required>
              <ProfileChoices profiles={profiles} />
            </Form.Field>
            <Form.Field
              name="machineNamespace"
              label="Machine namespace"
              description="Where this class's Machines live in the provider project."
              required>
              <Form.Input />
            </Form.Field>
            <Form.Field
              name="capacityPool"
              label="Capacity pool"
              description={`Machines labelled ${CAPACITY_POOL_LABEL} with this value can serve the class.`}
              required>
              <Form.Input placeholder="e.g. central-metal" />
            </Form.Field>
            <Form.Field name="powerControl">
              <Form.Checkbox label="Let consumers turn machines on and off" />
            </Form.Field>
          </div>
        </Form.Dialog>
      </ConformAdapter>
    </>
  );
}
