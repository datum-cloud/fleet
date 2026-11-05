import { FIXTURE_MACHINES, fixtureProfiles } from './fixtures';
import {
  toMachine,
  toMachineProfile,
  type Machine,
  type MachineProfile,
  type RawMachineList,
  type RawMachineProfile,
  type RawMachineProfileList,
} from '../types';
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from '@tanstack/react-query';

export const PLUGIN_ID = 'fleet.staff-portal.datumapis.com';

// The single provider project and the namespace holding its Machines. The
// namespace is the proposal's example until the real one is decided.
export const PROVIDER_PROJECT = 'datum-cloud';
export const MACHINE_NAMESPACE = 'infra-ops';

const USE_FIXTURES = import.meta.env.VITE_FLEET_FIXTURES === 'true';
const REFETCH_INTERVAL_MS = 10_000;

const FLEET_API = '/apis/fleet.datumapis.com/v1alpha1';

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

function getProjectScopedBase(projectName: string): string {
  return `/apis/resourcemanager.miloapis.com/v1alpha1/projects/${encodeURIComponent(projectName)}/control-plane`;
}

// staff-portal's /api/internal proxy wraps every response as
// { requestId, code, data, path }; the upstream object is at `.data`, and a
// failure carries the upstream message in `.error`. The proxy forwards the
// staff member's own token, so writes need their own Fleet role.
interface ProxyEnvelope<T> {
  data?: T;
  error?: string;
}

async function proxyFetch<T>(
  projectName: string,
  path: string,
  init: { method?: string; body?: unknown } = {}
): Promise<T> {
  const url = `/api/internal${getProjectScopedBase(projectName)}${path}`;
  const res = await fetch(url, {
    method: init.method ?? 'GET',
    headers: {
      Accept: 'application/json',
      ...(init.body !== undefined && { 'Content-Type': 'application/json' }),
    },
    body: init.body !== undefined ? JSON.stringify(init.body) : undefined,
  });
  const envelope = (await res.json().catch(() => ({}))) as ProxyEnvelope<T>;
  if (!res.ok) {
    throw new ApiError(res.status, envelope.error || `Request failed (${res.status}): ${path}`);
  }
  return envelope.data as T;
}

export async function fetchMachines(): Promise<Machine[]> {
  if (USE_FIXTURES) return FIXTURE_MACHINES.map(toMachine);
  const path = `${FLEET_API}/namespaces/${MACHINE_NAMESPACE}/machines?limit=100`;
  const body = await proxyFetch<RawMachineList>(PROVIDER_PROJECT, path);
  return (body.items ?? []).map(toMachine);
}

export function useMachines(): UseQueryResult<Machine[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'machines', PROVIDER_PROJECT, MACHINE_NAMESPACE],
    queryFn: fetchMachines,
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

// MachineProfiles are cluster-scoped in the provider project's control plane.

export async function fetchMachineProfiles(): Promise<MachineProfile[]> {
  if (USE_FIXTURES) return fixtureProfiles.list().map(toMachineProfile);
  const body = await proxyFetch<RawMachineProfileList>(
    PROVIDER_PROJECT,
    `${FLEET_API}/machineprofiles?limit=100`
  );
  return (body.items ?? []).map(toMachineProfile);
}

export function useMachineProfiles(): UseQueryResult<MachineProfile[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'machineprofiles', PROVIDER_PROJECT],
    queryFn: fetchMachineProfiles,
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

export async function createMachineProfile(profile: RawMachineProfile): Promise<RawMachineProfile> {
  if (USE_FIXTURES) return fixtureProfiles.create(profile);
  return proxyFetch<RawMachineProfile>(PROVIDER_PROJECT, `${FLEET_API}/machineprofiles`, {
    method: 'POST',
    body: { apiVersion: 'fleet.datumapis.com/v1alpha1', kind: 'MachineProfile', ...profile },
  });
}

export function useCreateMachineProfile(): UseMutationResult<
  RawMachineProfile,
  ApiError,
  RawMachineProfile
> {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: createMachineProfile,
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'machineprofiles', PROVIDER_PROJECT] }),
  });
}
