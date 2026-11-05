import { FIXTURE_CLASSES, FIXTURE_PROFILES, fixtureClaims } from './fixtures';
import {
  toCatalog,
  toClaimDetail,
  toMachineClaim,
  type ClaimDetail,
  type CatalogOffering,
  type MachineClaim,
  type RawMachineClaim,
  type RawMachineClaimList,
  type RawMachineClassList,
  type RawMachineProfileList,
} from '../types';
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from '@tanstack/react-query';

export const PLUGIN_ID = 'fleet.datumapis.com';

const USE_FIXTURES = import.meta.env.VITE_FLEET_FIXTURES === 'true';
const REFETCH_INTERVAL_MS = 10_000;

const FLEET_API = '/apis/fleet.datumapis.com/v1alpha1';
const CLAIM_NAMESPACE = 'default';
const MACHINECLAIMS_PATH = `${FLEET_API}/namespaces/${CLAIM_NAMESPACE}/machineclaims`;

/** The only location offered for now. */
export const DEFAULT_LOCATION = 'us-central-1';

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

// The portal proxy returns the upstream body unwrapped; a Kubernetes failure
// is a Status object whose `message` says what went wrong.
async function proxyFetch<T>(
  projectId: string,
  path: string,
  init: { method?: string; body?: unknown } = {}
): Promise<T> {
  const res = await fetch(`${getProjectScopedBase(projectId)}${path}`, {
    method: init.method ?? 'GET',
    headers: {
      Accept: 'application/json',
      ...(init.body !== undefined && { 'Content-Type': 'application/json' }),
    },
    body: init.body !== undefined ? JSON.stringify(init.body) : undefined,
  });
  if (!res.ok) {
    const status = (await res.json().catch(() => ({}))) as { message?: string };
    throw new ApiError(res.status, status.message || `Request failed (${res.status}): ${path}`);
  }
  return res.json() as Promise<T>;
}

export async function fetchMachineClaims(projectId: string): Promise<MachineClaim[]> {
  if (USE_FIXTURES) return fixtureClaims.list().map(toMachineClaim);
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

// MachineClasses and MachineProfiles are cluster-scoped in the project's
// control plane: the project's own definitions or ones published into it.
export async function fetchCatalog(projectId: string): Promise<CatalogOffering[]> {
  if (USE_FIXTURES) return toCatalog(FIXTURE_CLASSES, FIXTURE_PROFILES);
  const [classes, profiles] = await Promise.all([
    proxyFetch<RawMachineClassList>(projectId, `${FLEET_API}/machineclasses?limit=100`),
    proxyFetch<RawMachineProfileList>(projectId, `${FLEET_API}/machineprofiles?limit=100`),
  ]);
  return toCatalog(classes.items ?? [], profiles.items ?? []);
}

export function useCatalog(projectId: string | undefined): UseQueryResult<CatalogOffering[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'catalog', projectId],
    enabled: !!projectId,
    queryFn: () => fetchCatalog(projectId as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

export async function fetchMachineClaim(projectId: string, name: string): Promise<ClaimDetail> {
  if (USE_FIXTURES) {
    const claim = fixtureClaims.list().find((c) => c.metadata.name === name);
    if (!claim) throw new ApiError(404, `machineclaims "${name}" not found`);
    return toClaimDetail(claim);
  }
  const claim = await proxyFetch<RawMachineClaim>(
    projectId,
    `${MACHINECLAIMS_PATH}/${encodeURIComponent(name)}`
  );
  return toClaimDetail(claim);
}

export function useMachineClaim(
  projectId: string | undefined,
  name: string | undefined
): UseQueryResult<ClaimDetail, ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'machineclaims', projectId, name],
    enabled: !!projectId && !!name,
    queryFn: () => fetchMachineClaim(projectId as string, name as string),
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

export interface CreateClaimInput {
  name: string;
  machineClass: string;
  machineProfile: string;
}

// Deploying allocates a machine and hands it over powered off; installing an
// OS is a separate Installation, which this plugin does not offer yet.
export function toRawMachineClaim(input: CreateClaimInput): RawMachineClaim {
  return {
    metadata: { name: input.name, namespace: CLAIM_NAMESPACE },
    spec: {
      machineClassRef: { name: input.machineClass },
      machineProfileRef: { name: input.machineProfile },
      locationRef: { name: DEFAULT_LOCATION },
      powerState: 'Off',
    },
  };
}

export async function createMachineClaim(
  projectId: string,
  input: CreateClaimInput
): Promise<RawMachineClaim> {
  const claim = toRawMachineClaim(input);
  if (USE_FIXTURES) return fixtureClaims.create(claim);
  return proxyFetch<RawMachineClaim>(projectId, MACHINECLAIMS_PATH, {
    method: 'POST',
    body: { apiVersion: 'fleet.datumapis.com/v1alpha1', kind: 'MachineClaim', ...claim },
  });
}

export function useCreateMachineClaim(
  projectId: string | undefined
): UseMutationResult<RawMachineClaim, ApiError, CreateClaimInput> {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input) => createMachineClaim(projectId as string, input),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'machineclaims', projectId] }),
  });
}
