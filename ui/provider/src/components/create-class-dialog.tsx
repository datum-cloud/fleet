import type { ClassCapacities } from './class-table';
import { CAPACITY_POOL_LABEL, type MachineProfile, type RawMachineClass } from '../types';
import { useCreateMachineClass, useMachineProfiles } from '../lib/api';
import { Alert, AlertDescription } from '@datum-cloud/datum-ui/alert';
import { Button } from '@datum-cloud/datum-ui/button';
import { Checkbox } from '@datum-cloud/datum-ui/checkbox';
import { Form } from '@datum-cloud/datum-ui/form';
import { ConformAdapter } from '@datum-cloud/datum-ui/form/adapters/conform';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Label } from '@datum-cloud/datum-ui/label';
import { Text } from '@datum-cloud/datum-ui/typography';
import { PlusIcon } from 'lucide-react';
import { useState } from 'react';
import { z } from 'zod';

const NAME_PATTERN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const NAME_MESSAGE =
  'Lowercase letters, numbers, and hyphens only. Must start and end with a letter or number.';

// A class is the offering: the profiles consumers may deploy and the pool of
// MachineCapacities that serves them. Custom profiles and console access stay off;
// the first implementation supports power control but no console sessions.
export const classFormSchema = z.object({
  name: z.string({ error: 'Name is required.' }).max(63, 'At most 63 characters.').regex(NAME_PATTERN, NAME_MESSAGE),
  allowedProfiles: z.array(z.string(), { error: 'Choose at least one profile.' }).min(1, 'Choose at least one profile.'),
  // "<namespace>/<pool>", chosen from the pools that MachineCapacities carry.
  capacity: z
    .string({ error: 'Choose a capacity pool.' })
    .regex(/^[^/]+\/[^/]+$/, 'Choose a capacity pool.'),
  powerControl: z.boolean().optional(),
});

export type ClassFormValues = z.infer<typeof classFormSchema>;

export function toRawMachineClass(values: ClassFormValues): RawMachineClass {
  const [namespace, pool] = values.capacity.split('/');
  return {
    metadata: { name: values.name },
    spec: {
      profilePolicy: {
        allowedProfiles: values.allowedProfiles.map((name) => ({ name })),
        customProfiles: { enabled: false },
      },
      access: { consoleModes: [], powerControl: !!values.powerControl },
      capacity: {
        machineCapacities: {
          namespace,
          selector: { matchLabels: { [CAPACITY_POOL_LABEL]: pool } },
        },
      },
    },
  };
}

function ProfileChoices({ profiles }: { profiles: MachineProfile[] | undefined }) {
  if (!profiles?.length) {
    return (
      <Text as="p" textColor="muted">
        No profiles yet. Create one on the Profiles tab first.
      </Text>
    );
  }
  // Checkboxes sharing a name submit as an array, which the schema parses.
  return (
    <div className="flex flex-col gap-2">
      {profiles.map((profile) => (
        <Label key={profile.name} className="flex items-center gap-2">
          <Checkbox name="allowedProfiles" value={profile.name} />
          <Text weight="medium">{profile.name}</Text>
          <Text textColor="muted">
            {profile.cpu} · {profile.memory}
          </Text>
        </Label>
      ))}
    </div>
  );
}

export interface CapacityPool { namespace: string; pool: string; count: number }

// The pools that existing MachineCapacities carry, with how many each holds.
// A class picks one of these, so a typo cannot select nothing.
export function capacityPools(capacities: ClassCapacities): CapacityPool[] {
  if (!Array.isArray(capacities)) return [];
  const pools = new Map<string, CapacityPool>();
  for (const c of capacities) {
    if (!c.pool || !c.namespace) continue;
    const key = `${c.namespace}/${c.pool}`;
    pools.set(key, { namespace: c.namespace, pool: c.pool, count: (pools.get(key)?.count ?? 0) + 1 });
  }
  return [...pools.values()].sort((a, b) => a.pool.localeCompare(b.pool) || a.namespace.localeCompare(b.namespace));
}

function PoolChoices({ capacities }: { capacities: ClassCapacities }) {
  const pools = capacityPools(capacities);
  if (!pools.length) {
    return (
      <Text as="p" textColor="muted">
        {capacities === 'unknown' ? 'Capacity pools are unknown: capacity could not be read.' : 'No capacity carries a pool label yet.'}
      </Text>
    );
  }
  // The namespace shows only when two namespaces share a pool name.
  const shared = (pool: string) => pools.filter((p) => p.pool === pool).length > 1;
  return (
    <Form.Select placeholder="Choose a pool">
      {pools.map((p) => (
        <Form.SelectItem key={`${p.namespace}/${p.pool}`} value={`${p.namespace}/${p.pool}`}>
          {p.pool}{shared(p.pool) ? ` (${p.namespace})` : ''} · {p.count}
        </Form.SelectItem>
      ))}
    </Form.Select>
  );
}

// Errors render in the dialog because staff-portal does not share its toast
// module with plugins.
export function CreateClassDialog({ capacities }: { capacities: ClassCapacities }) {
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
          description="An offering consumers can deploy from: the profiles it allows and the capacity that serves it."
          schema={classFormSchema}
          defaultValues={{ powerControl: true }}
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
            <Form.Field name="capacity" label="Capacity pool" required>
              <PoolChoices capacities={capacities} />
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
