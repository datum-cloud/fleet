import { FIXTURE_CLASSES, FIXTURE_PROFILES, fixtureMachines } from './fixtures';
import {
  toCatalog,
  toMachineDetail,
  toMachine,
  type MachineDetail,
  type CatalogOffering,
  type Machine,
  type RawMachine,
  type RawMachineList,
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

export const PLUGIN_ID = 'metal.datumapis.com';

const USE_FIXTURES = import.meta.env.VITE_FLEET_FIXTURES === 'true';
const REFETCH_INTERVAL_MS = 10_000;
// Faster while a Machine is still changing, so Ready or a finished release
// shows up without a reload.
const SETTLING_INTERVAL_MS = 3_000;

const settling = (machines: Pick<Machine, 'phase'>[] = []) =>
  machines.some((m) => m.phase === 'Pending' || m.phase === 'Bound' || m.phase === 'Releasing');

// cloud-portal's QueryClient turns refetchOnWindowFocus off for every query,
// and polling pauses in a background tab, so without this a Machine that
// changed while the tab was hidden stays stale until a reload.
const MACHINE_QUERY = { refetchOnWindowFocus: 'always' as const, retry: false };

const METAL_API = '/apis/metal.datumapis.com/v1alpha1';
const MACHINE_NAMESPACE = 'default';
const MACHINES_PATH = `${METAL_API}/namespaces/${MACHINE_NAMESPACE}/machines`;

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

export async function fetchMachines(projectId: string): Promise<Machine[]> {
  if (USE_FIXTURES) return fixtureMachines.list().map(toMachine);
  const body = await proxyFetch<RawMachineList>(projectId, `${MACHINES_PATH}?limit=100`);
  return (body.items ?? []).map(toMachine);
}

export function useMachines(
  projectId: string | undefined
): UseQueryResult<Machine[], ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'machines', projectId],
    enabled: !!projectId,
    queryFn: () => fetchMachines(projectId as string),
    refetchInterval: (query) => (settling(query.state.data) ? SETTLING_INTERVAL_MS : REFETCH_INTERVAL_MS),
    ...MACHINE_QUERY,
  });
}

// MachineClasses and MachineProfiles are cluster-scoped in the project's
// control plane: the project's own definitions or ones published into it.
export async function fetchCatalog(projectId: string): Promise<CatalogOffering[]> {
  if (USE_FIXTURES) return toCatalog(FIXTURE_CLASSES, FIXTURE_PROFILES);
  const [classes, profiles] = await Promise.all([
    proxyFetch<RawMachineClassList>(projectId, `${METAL_API}/machineclasses?limit=100`),
    proxyFetch<RawMachineProfileList>(projectId, `${METAL_API}/machineprofiles?limit=100`),
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

export async function fetchMachine(projectId: string, name: string): Promise<MachineDetail> {
  if (USE_FIXTURES) {
    const machine = fixtureMachines.list().find((c) => c.metadata.name === name);
    if (!machine) throw new ApiError(404, `machines "${name}" not found`);
    return toMachineDetail(machine);
  }
  const machine = await proxyFetch<RawMachine>(
    projectId,
    `${MACHINES_PATH}/${encodeURIComponent(name)}`
  );
  return toMachineDetail(machine);
}

export function useMachine(
  projectId: string | undefined,
  name: string | undefined
): UseQueryResult<MachineDetail, ApiError> {
  return useQuery({
    queryKey: [PLUGIN_ID, 'machines', projectId, name],
    enabled: !!projectId && !!name,
    queryFn: () => fetchMachine(projectId as string, name as string),
    refetchInterval: (query) => {
      const machine = query.state.data;
      return machine && settling([machine]) ? SETTLING_INTERVAL_MS : REFETCH_INTERVAL_MS;
    },
    ...MACHINE_QUERY,
  });
}

export interface CreateMachineInput {
  name: string;
  machineClass: string;
  machineProfile: string;
}

// Deploying allocates a machine and hands it over powered off; installing an
// OS is a separate Installation, which this plugin does not offer yet.
export function toRawMachine(input: CreateMachineInput): RawMachine {
  return {
    metadata: { name: input.name, namespace: MACHINE_NAMESPACE },
    spec: {
      machineClassRef: { name: input.machineClass },
      machineProfileRef: { name: input.machineProfile },
      locationRef: { name: DEFAULT_LOCATION },
      powerState: 'Off',
    },
  };
}

export async function createMachine(
  projectId: string,
  input: CreateMachineInput
): Promise<RawMachine> {
  const machine = toRawMachine(input);
  if (USE_FIXTURES) return fixtureMachines.create(machine);
  return proxyFetch<RawMachine>(projectId, MACHINES_PATH, {
    method: 'POST',
    body: { apiVersion: 'metal.datumapis.com/v1alpha1', kind: 'Machine', ...machine },
  });
}

export function useCreateMachine(
  projectId: string | undefined
): UseMutationResult<RawMachine, ApiError, CreateMachineInput> {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input) => createMachine(projectId as string, input),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'machines', projectId] }),
  });
}

// Releasing deletes the machine. Fleet's finalizer keeps it, listed as
// Releasing, until its capacity is freed; the Machine then disappears.
export async function releaseMachine(projectId: string, name: string): Promise<void> {
  if (USE_FIXTURES) return fixtureMachines.release(name);
  await proxyFetch<unknown>(projectId, `${MACHINES_PATH}/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  });
}

export function useReleaseMachine(
  projectId: string | undefined
): UseMutationResult<void, ApiError, string> {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (name) => releaseMachine(projectId as string, name),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: [PLUGIN_ID, 'machines', projectId] }),
  });
}
