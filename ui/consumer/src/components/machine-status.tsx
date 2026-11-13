import { machineStatus, type Machine, type StatusTone } from '../types';
import { SpinnerIcon } from '@datum-cloud/datum-ui/icons';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';

type StatusInput = Pick<Machine, 'phase' | 'phaseReason' | 'waitingForCapacity'>;

export function Dot({ tone }: { tone: StatusTone }) {
  return <span aria-hidden className="fleet-dot" data-tone={tone} />;
}

// A phase still moving (waiting, preparing, releasing) spins instead of
// showing a dot.
function Mark({ machine, tone }: { machine: StatusInput; tone: StatusTone }) {
  const moving = machine.phase === 'Pending' || machine.phase === 'Bound' || machine.phase === 'Releasing';
  return moving ? <SpinnerIcon size="xs" aria-hidden /> : <Dot tone={tone} />;
}

/** Dot and label, for table cells. Fleet's reason shows on hover. */
export function MachineStatus({ machine }: { machine: StatusInput }) {
  const { tone, label } = machineStatus(machine);
  return (
    <Tooltip message={machine.phaseReason} hidden={!machine.phaseReason}>
      <span
        className="inline-flex cursor-default items-center gap-2 text-sm whitespace-nowrap"
        data-testid="fleet-plugin-machine-status">
        <Mark machine={machine} tone={tone} />
        {label}
      </span>
    </Tooltip>
  );
}

/** The same status as a chip, for the detail page's band. */
export function MachineStatusChip({
  machine,
  suffix,
}: {
  machine: StatusInput;
  /** Extra detail after the label, e.g. the observed power state. */
  suffix?: string;
}) {
  const { tone, label } = machineStatus(machine);
  return (
    <span className="fleet-chip shrink-0 cursor-default" data-testid="fleet-plugin-machine-status">
      <Mark machine={machine} tone={tone} />
      {suffix ? `${label} · ${suffix}` : label}
    </span>
  );
}
