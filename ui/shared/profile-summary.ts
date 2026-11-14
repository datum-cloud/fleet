// Shared by ui/consumer and ui/provider. Keep this directory free of package
// imports: each plugin has its own node_modules, and a bare import here would
// not resolve from either.

import type { MachineProfile } from './fleet-api';

type Bounds<T> = { min?: T; max?: T; exact?: T };

// Renders a min/max/exact constraint; an empty one means "any".
export function formatConstraint<T extends string | number>(
  c: Bounds<T> | undefined,
  unit = ''
): string {
  const u = (v: T) => `${v}${unit}`;
  if (!c) return 'Any';
  if (c.exact !== undefined) return u(c.exact);
  if (c.min !== undefined && c.max !== undefined) return `${u(c.min)}–${u(c.max)}`;
  if (c.min !== undefined) return `≥ ${u(c.min)}`;
  if (c.max !== undefined) return `≤ ${u(c.max)}`;
  return 'Any';
}

export interface ProfileSummary {
  /** e.g. "≥ 24 cores · amd64" */
  cpu: string;
  memory: string;
  /** Absent when the profile asks for no GPUs. */
  gpus?: string;
  disks: number;
  interfaces: number;
}

export function summarizeProfile(spec: MachineProfile['spec']): ProfileSummary {
  const { cpu, memory, gpus, disks, interfaces } = spec;
  const gpuWhat = gpus ? [gpus.vendor, gpus.models?.join(' / ')].filter(Boolean).join(' ') : '';
  return {
    cpu: `${formatConstraint(cpu.cores, ' cores')} · ${cpu.architecture}`,
    memory: formatConstraint(memory.capacity),
    gpus: gpus && (gpuWhat ? `${formatConstraint(gpus.count)} × ${gpuWhat}` : formatConstraint(gpus.count)),
    disks: disks?.length ?? 0,
    interfaces: interfaces?.length ?? 0,
  };
}
