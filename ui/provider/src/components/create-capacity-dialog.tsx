import { CAPACITY_POOL_LABEL, type MachineClass, type RawMachineCapacity } from '../types';
import { useCreateMachineCapacity, useMachineClasses } from '../lib/api';
import { Alert, AlertDescription } from '@datum-cloud/datum-ui/alert';
import { Button } from '@datum-cloud/datum-ui/button';
import { Form } from '@datum-cloud/datum-ui/form';
import { ConformAdapter } from '@datum-cloud/datum-ui/form/adapters/conform';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Text } from '@datum-cloud/datum-ui/typography';
import { PlusIcon } from 'lucide-react';
import { useState } from 'react';
import { z } from 'zod';

const NAME_PATTERN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const NAME_MESSAGE =
  'Lowercase letters, numbers, and hyphens only. Must start and end with a letter or number.';

const resourceName = (label: string) =>
  z.string({ error: `${label} is required.` }).max(63, 'At most 63 characters.').regex(NAME_PATTERN, NAME_MESSAGE);

// A class that draws from a namespace and capacity pool; only these can
// place new capacity where the class will find it.
export type CapacityTarget = MachineClass & { capacityNamespace: string; capacityPool: string };

export function capacityTargets(classes: MachineClass[] | undefined): CapacityTarget[] {
  return (classes ?? []).filter(
    (c): c is CapacityTarget => !c.source && !!c.capacityNamespace && !!c.capacityPool
  );
}

// The form fills in the rest of the spec. Fleet has no Device inventory yet,
// so the device reference is named after the capacity; it can't be changed
// later. The cleaning policy is the name Fleet's standalone fixtures use;
// nothing defines cleaning policies yet. Allocation stays off unless asked.
export const SYNTHETIC_CLEANING_POLICY = 'synthetic-tenant-reuse-v1';

export const capacityFormSchema = z.object({
  name: resourceName('Name'),
  machineClass: z.string({ error: 'Choose a class.' }).min(1, 'Choose a class.'),
  allocationEnabled: z.boolean().optional(),
});

export type CapacityFormValues = z.infer<typeof capacityFormSchema>;

export function toRawCapacity(values: CapacityFormValues, target: CapacityTarget): RawMachineCapacity {
  return {
    metadata: {
      name: values.name,
      namespace: target.capacityNamespace,
      labels: { [CAPACITY_POOL_LABEL]: target.capacityPool },
    },
    spec: {
      deviceRef: { name: values.name },
      allocationPolicy: values.allocationEnabled ? 'Enabled' : 'Disabled',
      cleaningPolicyRef: { name: SYNTHETIC_CLEANING_POLICY },
      reclaimPolicy: 'Retain',
    },
  };
}

function Placement({ targets }: { targets: CapacityTarget[] }) {
  const selected = Form.useWatch<string>('machineClass');
  const target = targets.find((t) => t.name === selected);
  if (!target) return null;
  return (
    <Text as="p" textColor="muted" data-testid="fleet-plugin-capacity-placement">
      Created in namespace{' '}
      <Text textColor="default" weight="medium">
        {target.capacityNamespace}
      </Text>{' '}
      with capacity pool{' '}
      <Text textColor="default" weight="medium">
        {target.capacityPool}
      </Text>
      .
    </Text>
  );
}

// Errors render in the dialog because staff-portal does not share its toast
// module with plugins.
export function CreateCapacityDialog() {
  const [open, setOpen] = useState(false);
  const mutation = useCreateMachineCapacity();
  const { data: classes } = useMachineClasses();
  const targets = capacityTargets(classes);

  const setOpenAndReset = (next: boolean) => {
    if (next) mutation.reset();
    setOpen(next);
  };

  const handleSubmit = (values: CapacityFormValues) => {
    const target = targets.find((t) => t.name === values.machineClass);
    if (!target) return;
    mutation.mutate(toRawCapacity(values, target), { onSuccess: () => setOpen(false) });
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
        Create capacity
      </Button>
      <ConformAdapter>
        <Form.Dialog
          open={open}
          onOpenChange={setOpenAndReset}
          title="Create machine capacity"
          description="Registers a synthetic machine for a class to draw from. Fleet fills in its hardware and health once discovery reports them."
          schema={capacityFormSchema}
          defaultValues={{ allocationEnabled: false }}
          onSubmit={handleSubmit}
          loading={mutation.isPending}
          submitText="Create"
          submitTextLoading="Creating..."
          // The dialog caps its height, but the form inside doesn't shrink,
          // so a long form pushes the footer out of view. A flex column lets
          // the body scroll instead.
          formClassName="flex min-h-0 flex-col"
          className="w-full focus:ring-0 focus:outline-none sm:max-w-lg">
          <div className="space-y-4 px-5" data-testid="fleet-plugin-create-capacity-form">
            {mutation.error && (
              <Alert variant="destructive">
                <AlertDescription>{mutation.error.message}</AlertDescription>
              </Alert>
            )}
            <Form.Field name="name" label="Name" required>
              <Form.Input autoFocus placeholder="e.g. central1-baremetal-04" />
            </Form.Field>
            <Form.Field
              name="machineClass"
              label="Class"
              description="It goes in this class's namespace and capacity pool."
              required>
              {targets.length > 0 ? (
                <Form.Select placeholder="Choose a class">
                  {targets.map((t) => (
                    <Form.SelectItem key={t.name} value={t.name}>
                      {t.name}
                    </Form.SelectItem>
                  ))}
                </Form.Select>
              ) : (
                <Text as="p" textColor="muted">
                  No classes with a capacity pool yet. Create one on the Classes tab first.
                </Text>
              )}
            </Form.Field>
            <Placement targets={targets} />
            <Form.Field name="allocationEnabled">
              <Form.Checkbox label="Offer for allocation" />
            </Form.Field>
          </div>
        </Form.Dialog>
      </ConformAdapter>
    </>
  );
}
