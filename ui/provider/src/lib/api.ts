import { fixtureClasses, fixtureCapacities, fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords, fixtureProfiles } from './fixtures';
import { toInventory, inventoryResources, type InventoryKind, type Inventory, type RawDeviceClaimList, type RawDeviceList } from '../inventory';
import { useInventoryRead } from './inventory-read';
import type { List, PhysicalRecord } from '../../../shared/fleet-api';
import {
  toMachineCapacity,
  toMachineClass,
  toMachineProfile,
  type MachineCapacity,
  type MachineClass,
  type MachineProfile,
  type RawMachineCapacity,
  type RawMachineClass,
  type RawMachineClassList,
  type RawMachineCapacityList,
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

export const PLUGIN_ID = 'metal.staff-portal.datumapis.com';

// The single provider project. Its MachineCapacities can live in any namespace: each
// class names the namespace and capacity pool it draws from.
export const PROVIDER_PROJECT = 'datum-cloud';

const USE_FIXTURES = import.meta.env.VITE_FLEET_FIXTURES === 'true';
const REFETCH_INTERVAL_MS = 10_000;

const METAL_API = '/apis/metal.datumapis.com/v1alpha1';
const INVENTORY_API = '/apis/fleet.datumapis.com/v1alpha1';

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

export async function proxyFetch<T>(
  projectName: string,
  path: string,
  init: { method?: string; body?: unknown; contentType?: string } = {}
): Promise<T> {
  const url = `/api/internal${getProjectScopedBase(projectName)}${path}`;
  const res = await fetch(url, {
    method: init.method ?? 'GET',
    headers: {
      Accept: 'application/json',
      ...(init.body !== undefined && { 'Content-Type': init.contentType ?? 'application/json' }),
    },
    body: init.body !== undefined ? JSON.stringify(init.body) : undefined,
  });
  const envelope = (await res.json().catch(() => ({}))) as ProxyEnvelope<T>;
  if (!res.ok) {
    throw new ApiError(res.status, envelope.error || `Request failed (${res.status}): ${path}`);
  }
  return envelope.data as T;
}

// MachineCapacities are namespaced; the list spans every namespace in the project.
export async function fetchMachineCapacities(): Promise<MachineCapacity[]> {
  if (USE_FIXTURES) return fixtureCapacities.list().map(toMachineCapacity);
  const body = await proxyFetch<RawMachineCapacityList>(PROVIDER_PROJECT, `${METAL_API}/machinecapacities?limit=100`);
  return (body.items ?? []).map(toMachineCapacity);
}

export function useMachineCapacities(): UseQueryResult<MachineCapacity[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'machinecapacities', PROVIDER_PROJECT],
    queryFn: fetchMachineCapacities,
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

export async function createMachineCapacity(capacity: RawMachineCapacity): Promise<RawMachineCapacity> {
  if (USE_FIXTURES) return fixtureCapacities.create(capacity);
  const namespace = encodeURIComponent(capacity.metadata.namespace ?? '');
  return proxyFetch<RawMachineCapacity>(PROVIDER_PROJECT, `${METAL_API}/namespaces/${namespace}/machinecapacities`, {
    method: 'POST',
    body: { apiVersion: 'metal.datumapis.com/v1alpha1', kind: 'MachineCapacity', ...capacity },
  });
}

export function useCreateMachineCapacity(): UseMutationResult<RawMachineCapacity, ApiError, RawMachineCapacity> {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: createMachineCapacity,
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'machinecapacities', PROVIDER_PROJECT] }),
  });
}

export async function deleteMachineCapacity(capacity: { namespace?: string; name: string }): Promise<void> {
  if (USE_FIXTURES) return fixtureCapacities.delete(capacity.namespace ?? '', capacity.name);
  const namespace = encodeURIComponent(capacity.namespace ?? '');
  await proxyFetch<unknown>(
    PROVIDER_PROJECT,
    `${METAL_API}/namespaces/${namespace}/machinecapacities/${encodeURIComponent(capacity.name)}`,
    { method: 'DELETE' }
  );
}

export function useDeleteMachineCapacity(): UseMutationResult<
  void,
  ApiError,
  { namespace?: string; name: string }
> {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: deleteMachineCapacity,
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'machinecapacities', PROVIDER_PROJECT] }),
  });
}

// MachineProfiles are cluster-scoped in the provider project's control plane.

export async function fetchMachineProfiles(): Promise<MachineProfile[]> {
  if (USE_FIXTURES) return fixtureProfiles.list().map(toMachineProfile);
  const body = await proxyFetch<RawMachineProfileList>(
    PROVIDER_PROJECT,
    `${METAL_API}/machineprofiles?limit=100`
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
  return proxyFetch<RawMachineProfile>(PROVIDER_PROJECT, `${METAL_API}/machineprofiles`, {
    method: 'POST',
    body: { apiVersion: 'metal.datumapis.com/v1alpha1', kind: 'MachineProfile', ...profile },
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

export async function deleteMachineProfile(name: string): Promise<void> {
  if (USE_FIXTURES) return fixtureProfiles.delete(name);
  await proxyFetch<unknown>(
    PROVIDER_PROJECT,
    `${METAL_API}/machineprofiles/${encodeURIComponent(name)}`,
    { method: 'DELETE' }
  );
}

export function useDeleteMachineProfile(): UseMutationResult<void, ApiError, string> {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: deleteMachineProfile,
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'machineprofiles', PROVIDER_PROJECT] }),
  });
}

// MachineClasses are cluster-scoped too. A class here is the provider's
// definition: which profiles it allows and which MachineCapacities back it.

export async function fetchMachineClasses(): Promise<MachineClass[]> {
  if (USE_FIXTURES) return fixtureClasses.list().map(toMachineClass);
  const body = await proxyFetch<RawMachineClassList>(
    PROVIDER_PROJECT,
    `${METAL_API}/machineclasses?limit=100`
  );
  return (body.items ?? []).map(toMachineClass);
}

export function useMachineClasses(): UseQueryResult<MachineClass[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'machineclasses', PROVIDER_PROJECT],
    queryFn: fetchMachineClasses,
    refetchInterval: REFETCH_INTERVAL_MS,
    retry: false,
  });
}

export async function createMachineClass(machineClass: RawMachineClass): Promise<RawMachineClass> {
  if (USE_FIXTURES) return fixtureClasses.create(machineClass);
  return proxyFetch<RawMachineClass>(PROVIDER_PROJECT, `${METAL_API}/machineclasses`, {
    method: 'POST',
    body: { apiVersion: 'metal.datumapis.com/v1alpha1', kind: 'MachineClass', ...machineClass },
  });
}

export function useCreateMachineClass(): UseMutationResult<
  RawMachineClass,
  ApiError,
  RawMachineClass
> {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: createMachineClass,
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'machineclasses', PROVIDER_PROJECT] }),
  });
}

export async function deleteMachineClass(name: string): Promise<void> {
  if (USE_FIXTURES) return fixtureClasses.delete(name);
  await proxyFetch<unknown>(
    PROVIDER_PROJECT,
    `${METAL_API}/machineclasses/${encodeURIComponent(name)}`,
    { method: 'DELETE' }
  );
}

export function useDeleteMachineClass(): UseMutationResult<void, ApiError, string> {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: deleteMachineClass,
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'machineclasses', PROVIDER_PROJECT] }),
  });
}

// DeviceClaims and Devices are namespaced provider inventory, read across every
// namespace of the provider project. Staff need a role with
// fleet.datumapis.com/deviceclaims.list and devices.list there, such as
// fleet.datumapis.com-inventory-viewer.

export async function fetchInventory(): Promise<Inventory> {
  if (USE_FIXTURES) return toInventory(fixturePhysicalClaims, fixturePhysicalDevices, fixturePhysicalRecords);
  const issues: Inventory['issues'] = {};
  const results = await Promise.all(Object.entries(inventoryResources).map(async ([kind, plural]) => {
    try { return { kind, records: await fetchInventoryCollection<PhysicalRecord>(plural) }; }
    catch (error) {
      if (error instanceof ApiError && error.status === 401) throw error;
      issues[kind as InventoryKind] = { status: error instanceof ApiError ? error.status : 0 };
      return { kind, records: [] };
    }
  }));
  const claims = results.find(result => result.kind === 'DeviceClaim')!.records as unknown as NonNullable<RawDeviceClaimList['items']>;
  const devices = results.find(result => result.kind === 'Device')!.records as unknown as NonNullable<RawDeviceList['items']>;
  return { ...toInventory(claims, devices, results.filter(result => result.kind !== 'Device' && result.kind !== 'DeviceClaim').flatMap(result => result.records)), issues };
}

export async function fetchInventoryCollection<T>(plural: string): Promise<T[]> {
  const items: T[] = [];
  const seen = new Set<string>();
  let continuation = '';
  do {
    const page = await proxyFetch<List<T> & { metadata?: { continue?: string } }>(PROVIDER_PROJECT, `${INVENTORY_API}/${plural}?limit=500${continuation ? `&continue=${encodeURIComponent(continuation)}` : ''}`);
    items.push(...(page.items ?? []));
    continuation = page.metadata?.continue ?? '';
    if (continuation && seen.has(continuation)) throw new ApiError(502, 'Inventory pagination repeated; completeness Unknown');
    seen.add(continuation);
  } while (continuation);
  return items;
}

export function useInventory(): UseQueryResult<Inventory, ApiError> {
  const read = useInventoryRead(fetchInventory);
  return useQuery({
    queryKey: [PLUGIN_ID, 'inventory', PROVIDER_PROJECT],
    queryFn: read,
    refetchInterval: false,
    refetchOnWindowFocus: false,
    gcTime: 300_000,
    staleTime: Infinity,
    retry: false,
  });
}
