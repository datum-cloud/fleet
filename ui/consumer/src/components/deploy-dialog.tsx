import { Chips, optionKey, specChips, type DeployOption } from './deploy-options';
import { usePluginBase } from './machines-page';
import { ApiError, useCreateMachine } from '../lib/api';
import { Form } from '@datum-cloud/datum-ui/form';
import { ConformAdapter } from '@datum-cloud/datum-ui/form/adapters/conform';
import { toast } from '@datum-cloud/datum-ui/toast';
import { Text } from '@datum-cloud/datum-ui/typography';
import { useNavigate, useParams } from 'react-router';
import { z } from 'zod';

const NAME_MESSAGE =
  'Lowercase letters, numbers, and hyphens only. Must start and end with a letter or number.';

export const deployFormSchema = z.object({
  option: z.string({ error: 'Choose a profile.' }).min(1, 'Choose a profile.'),
  name: z
    .string({ error: 'Name is required.' })
    .max(63, 'At most 63 characters.')
    .regex(/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/, NAME_MESSAGE),
});

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? '' : 's'}`;

// The chosen profile's hardware as one chip line, as on the profile cards.
// The select already names the profile and class.
function Summary({ options }: { options: DeployOption[] }) {
  const key = Form.useWatch<string>('option');
  const option = options.find((o) => optionKey(o) === key);
  if (!option) return null;
  const { profile } = option;
  const chips = [
    ...specChips(profile),
    ...(profile.disks ? [plural(profile.disks, 'disk')] : []),
    ...(profile.interfaces ? [plural(profile.interfaces, 'NIC')] : []),
  ];
  return (
    <div data-testid="fleet-plugin-deploy-summary">
      <Chips chips={chips} />
    </div>
  );
}

// Deploying creates a Machine. It doesn't reserve capacity: the Machine
// starts Pending and Fleet assigns a server when one is free.
export function DeployDialog({
  open,
  options,
  preset,
  onOpenChange,
}: {
  open: boolean;
  options: DeployOption[];
  /** optionKey of the profile card that opened the dialog. */
  preset?: string;
  onOpenChange: (open: boolean) => void;
}) {
  const { projectId } = useParams<{ projectId: string }>();
  const base = usePluginBase();
  const navigate = useNavigate();
  const mutation = useCreateMachine(projectId);
  // One profile needs no choice.
  const initial = preset ?? (options.length === 1 ? optionKey(options[0]) : undefined);

  const handleSubmit = ({ name, option: key }: z.infer<typeof deployFormSchema>) => {
    const option = options.find((o) => optionKey(o) === key);
    if (!option) return;
    mutation.mutate(
      { name, machineClass: option.className, machineProfile: option.profile.name },
      {
        onSuccess: () => {
          toast.success(`Requested machine ${name}`);
          onOpenChange(false);
          navigate(`${base}/${encodeURIComponent(name)}`);
        },
        // The API's text names resources, groups and paths, so it isn't
        // shown. A 403 is the common case (no consumer role).
        onError: (error) => {
          const status = error instanceof ApiError ? error.status : undefined;
          if (status === 403) {
            toast.error("You don't have permission to deploy machines in this project.");
          } else if (status === 409) {
            toast.error(`A machine named ${name} already exists.`);
          } else {
            toast.error("Couldn't deploy the machine.");
          }
        },
      }
    );
  };

  return (
    <ConformAdapter>
      <Form.Dialog
        key={initial ?? 'none'}
        open={open}
        onOpenChange={onOpenChange}
        title="Deploy a machine"
        schema={deployFormSchema}
        defaultValues={{ option: initial }}
        onSubmit={handleSubmit}
        loading={mutation.isPending}
        submitText="Deploy"
        submitTextLoading="Deploying..."
        formClassName="flex min-h-0 flex-col"
        className="w-full focus:ring-0 focus:outline-none sm:max-w-xl">
        <div className="space-y-5 px-5" data-testid="fleet-plugin-deploy-form">
          <Form.Field name="option" label="Profile" required>
            {options.length > 0 ? (
              <Form.Select placeholder="Choose a profile">
                {options.map((o) => (
                  <Form.SelectItem key={optionKey(o)} value={optionKey(o)}>
                    {o.profile.name} · {o.className}
                  </Form.SelectItem>
                ))}
              </Form.Select>
            ) : (
              <Text as="p" textColor="muted">
                No machine classes are available to this project yet.
              </Text>
            )}
          </Form.Field>
          <Summary options={options} />
          <Form.Field
            name="name"
            label="Name"
            description="Can't be changed later."
            required>
            <Form.Input autoFocus placeholder="e.g. build-runner" />
          </Form.Field>
        </div>
      </Form.Dialog>
    </ConformAdapter>
  );
}
