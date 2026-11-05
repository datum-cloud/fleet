import { FIXTURE_MACHINES } from './fixtures';
import { toMachine, type Machine, type RawMachineList } from '../types';
import { useQuery, type UseQueryResult } from '@tanstack/react-query';

export const PLUGIN_ID = 'fleet.staff-portal.datumapis.com';

// The single provider project and the namespace holding its Machines. The
// namespace is the proposal's example until the real one is decided.
export const PROVIDER_PROJECT = 'datum-cloud';
export const MACHINE_NAMESPACE = 'infra-ops';

const USE_FIXTURES = import.meta.env.VITE_FLEET_FIXTURES === 'true';
const REFETCH_INTERVAL_MS = 10_000;

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
// { requestId, code, data, path }; the upstream object is at `.data`.
interface ProxyEnvelope<T> {
  data: T;
}

async function proxyFetch<T>(projectName: string, path: string): Promise<T> {
  const url = `/api/internal${getProjectScopedBase(projectName)}${path}`;
  const res = await fetch(url, { headers: { Accept: 'application/json' } });
  if (!res.ok) {
    throw new ApiError(res.status, `Request failed (${res.status}): ${path}`);
  }
  const envelope = (await res.json()) as ProxyEnvelope<T>;
  return envelope.data;
}

export async function fetchMachines(): Promise<Machine[]> {
  if (USE_FIXTURES) return FIXTURE_MACHINES.map(toMachine);
  const path = `/apis/fleet.datumapis.com/v1alpha1/namespaces/${MACHINE_NAMESPACE}/machines?limit=100`;
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
