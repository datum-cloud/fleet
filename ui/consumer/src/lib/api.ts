import { FIXTURE_CLAIMS } from './fixtures';
import { toMachineClaim, type MachineClaim, type RawMachineClaimList } from '../types';
import { useQuery, type UseQueryResult } from '@tanstack/react-query';

export const PLUGIN_ID = 'fleet.datumapis.com';

const USE_FIXTURES = import.meta.env.VITE_FLEET_FIXTURES === 'true';
const REFETCH_INTERVAL_MS = 10_000;

const MACHINECLAIMS_PATH = '/apis/fleet.datumapis.com/v1alpha1/namespaces/default/machineclaims';

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

function getProjectScopedBase(projectId: string): string {
  return `/api/proxy/apis/resourcemanager.miloapis.com/v1alpha1/projects/${encodeURIComponent(projectId)}/control-plane`;
}

async function proxyFetch<T>(projectId: string, path: string): Promise<T> {
  const res = await fetch(`${getProjectScopedBase(projectId)}${path}`, {
    headers: { Accept: 'application/json' },
  });
  if (!res.ok) {
    throw new ApiError(res.status, `Request failed (${res.status}): ${path}`);
  }
  return res.json() as Promise<T>;
}

export async function fetchMachineClaims(projectId: string): Promise<MachineClaim[]> {
  if (USE_FIXTURES) return FIXTURE_CLAIMS.map(toMachineClaim);
  const body = await proxyFetch<RawMachineClaimList>(projectId, `${MACHINECLAIMS_PATH}?limit=100`);
  return (body.items ?? []).map(toMachineClaim);
}

export function useMachineClaims(
  projectId: string | undefined
): UseQueryResult<MachineClaim[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'machineclaims', projectId],
    enabled: !!projectId,
    queryFn: () => fetchMachineClaims(projectId as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}
