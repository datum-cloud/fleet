import { machineStatus, reasonText, type Machine, type StatusTone } from '../types';
import { SpinnerIcon } from '@datum-cloud/datum-ui/icons';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import type { ReactNode } from 'react';

type StatusInput = Pick<Machine, 'phase' | 'phaseReason' | 'waitingForCapacity'>;
type PowerInput = Pick<Machine, 'powerIntent' | 'powerObserved'>;

export function Dot({ tone }: { tone: StatusTone }) {
  return <span aria-hidden className="fleet-dot" data-tone={tone} />;
}

// A phase still moving (waiting, preparing, releasing) spins instead of
// showing a dot.
function Mark({ machine, tone }: { machine: StatusInput; tone: StatusTone }) {
  const moving = machine.phase === 'Pending' || machine.phase === 'Bound' || machine.phase === 'Releasing';
  return moving ? <SpinnerIcon size="xs" aria-hidden /> : <Dot tone={tone} />;
}

// The reason's customer word, unless the label already says it. An unmapped
// reason shows as its raw token; a message never shows.
function statusReason(machine: StatusInput, label: string): string | undefined {
  if (!machine.phaseReason) return undefined;
  const { word, tooltip } = reasonText(machine.phaseReason, label);
  return word !== label ? word : tooltip;
}

/** Dot and label, for table cells. The reason shows on hover. */
export function MachineStatus({ machine }: { machine: StatusInput }) {
  const { tone, label } = machineStatus(machine);
  const reason = statusReason(machine, label);
  return (
    <Tooltip message={reason} hidden={!reason}>
      <span
        className="inline-flex cursor-default items-center gap-2 text-sm whitespace-nowrap"
        data-testid="fleet-plugin-machine-status"
        aria-label={reason ? `${label}: ${reason}` : undefined}>
        <Mark machine={machine} tone={tone} />
        {label}
      </span>
    </Tooltip>
  );
}

/**
 * Observed power, then the requested state in amber when it differs. Intent
 * alone never says the machine is on.
 */
export function Power({ machine, lower }: { machine: PowerInput; lower?: boolean }) {
  const { powerObserved, powerIntent } = machine;
  if (!powerObserved) return <>—</>;
  const say = (s: string) => (lower ? s.toLowerCase() : s);
  const differs = powerIntent !== powerObserved;
  return (
    <span
      data-testid="fleet-plugin-power"
      aria-label={differs ? `${say(powerObserved)}, requested ${say(powerIntent)}` : undefined}>
      {say(powerObserved)}
      {differs && <span className="fleet-warn"> → {say(powerIntent)}</span>}
    </span>
  );
}

/** The same status as a chip, for the detail page's band, with observed power. */
export function MachineStatusChip({
  machine,
  power,
  tooltip,
}: {
  machine: StatusInput & PowerInput;
  /** Show the observed power after the label. */
  power?: boolean;
  /** Extra hover detail, such as when power was reported. */
  tooltip?: ReactNode;
}) {
  const { tone, label } = machineStatus(machine);
  const reason = statusReason(machine, label);
  const message =
    reason || tooltip ? (
      <span className="flex flex-col gap-0.5">
        {reason && <span>{reason}</span>}
        {tooltip}
      </span>
    ) : undefined;
  return (
    <Tooltip message={message} hidden={!message}>
      <span className="fleet-chip shrink-0 cursor-default" data-testid="fleet-plugin-machine-status">
        <Mark machine={machine} tone={tone} />
        {label}
        {power && machine.powerObserved && (
          <>
            {' · powered '}
            <Power machine={machine} lower />
          </>
        )}
      </span>
    </Tooltip>
  );
}
