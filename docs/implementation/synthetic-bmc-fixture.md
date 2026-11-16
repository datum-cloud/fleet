# Synthetic BMC lifecycle fixture

Status: implemented test-only session-controller wiring and fixture executable.
Portable tests exercise actual private Coordinator custody. This is not a
production authorization adapter, provider observation source, or cleaning proof
for physical hardware. See [lifecycle custody](secure-bmc-lifecycle.md).

## Wiring and trust boundary

`fleet-controller` defaults to `--lifecycle-mode=production`: readiness fails and
no lifecycle authority is injected. An unknown mode fails startup. Explicit
`--lifecycle-mode=synthetic --private-namespace=fleet-system` injects the same
`LifecycleSessionAuthority` into both session and projection reconcilers. Its
Coordinator and resolver use an uncached Kubernetes client. The synthetic mode's
readiness indicates configured wiring, not that any particular fixture is ready.
Missing or invalid fixtures still deny session admission.

The standalone prototype manifest `config/manager/controller.yaml` opts into
synthetic mode. It is not a production overlay. Its RBAC reads namespaces and
reads/lists/updates Secrets only in `fleet-system`; it no longer reads allocation
CRDs. Changing the private namespace requires a corresponding scoped Role and
RoleBinding. The session controller cannot create fixture grants. The fixture
command is an operator tool, run with dedicated test-cluster credentials.

The fixture command reads existing Machine, MachineCapacity and namespace
lifetimes. It installs the normal Machine protection finalizer, retains an
immutable, private `synthetic-session-*` Secret grant, and calls
`EnsureClaimCustody` and `Allocate`. The grant alone never authorizes a session:
the real committed asset and bound claim custody must agree. The grant pins one
authenticated username, Machine UID/generation, namespace UIDs, capacity
UID/generation, device, location and member route. Every refresh rereads live
objects. Missing/recreated/deleting objects, changed device or generation, or a
different principal fail closed. Re-running allocation with different immutable
grant inputs is rejected.

Admission, class/profile requirements and current eligible hardware observations
are explicitly synthetic, as in the standalone/staging fixtures. The command
accepts only powered-off, networkless Machines without initial installation. It
constructs synthetic class/profile inputs matching the Machine's reference names;
those objects need not be published. It does not write availability status or use
MachineAllocation annotations. Real Coordinator CAS decides exclusivity even when
the synthetic evidence says capacity is available.

## Exact harness recipe

Build alongside the existing plugin and controller images, without needing a new
container or service:

```sh
go build -o "$E2E_DIR/bin/fleet-session-fixture" ./cmd/fleet-session-fixture
```

The E2E owner should apply `config/rbac/controller.yaml` and the prototype manager
manifest, create `fleet-provider`, and apply the Machine and capacity objects.
For example, the management API needs these synthetic objects:

```yaml
apiVersion: metal.datumapis.com/v1alpha1
kind: Machine
metadata: {name: db-01, namespace: default}
spec:
  machineClassRef: {name: synthetic-libvirt}
  machineProfileRef: {name: synthetic-libvirt}
  locationRef: {name: location-e2e}
  powerState: "Off"
---
apiVersion: metal.datumapis.com/v1alpha1
kind: MachineCapacity
metadata: {name: db01-capacity, namespace: fleet-provider}
spec:
  deviceRef: {name: device-allocated}
  allocationPolicy: Enabled
  cleaningPolicyRef: {name: synthetic-e2e}
  reclaimPolicy: Retain
```

Keep the existing location-local capacity/enrollment fixture aligned with the
capacity name and device. The command does not create provider enrollment or
federation registration. Then allocate:

```sh
fixture() {
  "$E2E_DIR/bin/fleet-session-fixture" --synthetic \
    --kubeconfig "$MGMT_KUBECONFIG" \
    --state "$E2E_DIR/session-executor.json" "$@"
}
fixture --phase=allocate --machine db-01 --capacity db01-capacity \
  --principal system:serviceaccount:default:fleet-authorized \
  --member-cluster fleet-location-e2e
```

Defaults are `--namespace=default`, `--capacity-namespace=fleet-provider` and
`--private-namespace=fleet-system`. The principal must be the exact username
retained by admission, not the local kubeconfig context name. Allocation prints
only `machineUID` and `epoch`, not the private fence token. Use equivalent calls
for db-02/db-03 rather than creating or patching MachineAllocation CRDs.

Retain the operator-local simulator state file outside Git across every retry and
reuse. A nonblocking file lock excludes concurrent fixture processes using the
same state path. Use exactly one state path/host per test environment. The
existing persistent simulator supplies **synthetic provider cleanup only**; it
cannot acknowledge gateway sessions or settle unknown Redfish effects.

## Release and recreation

Capture the old Machine UID before deletion. Never infer release authority from
a reused object name:

```sh
old_machine_uid=$(kubectl --kubeconfig "$MGMT_KUBECONFIG" -n default \
  get machine db-01 -o jsonpath='{.metadata.uid}')
fixture --phase=release --machine db-01 --machine-uid "$old_machine_uid"
```

With an active session, that call is expected to exit nonzero with
`management session teardown is not verified`, **after** the Coordinator has
transitioned the asset to Releasing and requested revocation. The harness must
assert this specific blocking result, not suppress arbitrary command errors.
The controller stops refresh, revokes projection, and consumes verified gateway
cleanup. Wait for stream closure and the binding's current-generation
`CleanupConfirmed=True`, then retry the exact command. Only a successful release
with `SafeToRemoveFinalizer=true` runs synthetic cleanup, settles claim custody,
and removes the Machine finalizer. No fixture command fabricates that condition.

After successful release, delete and recreate the Machine, then run allocation
again with its new UID. Reusing the same capacity retains the physical asset
record and increments its epoch; a different capacity must first finish its own
release. Old session bindings remain pinned to their old allocation and Machine
UID. A released Machine lifetime cannot be reallocated without recreation.

The E2E owner's script changes are:

- Replace initial and extra MachineAllocation seed/patch blocks with fixture
  allocation commands against the existing capacity objects.
- Replace release status patches with the blocked-release, verified-teardown,
  retry sequence above. Preserve the exact old UID through retries.
- Before an `up.sh` reset deletes Machines/capacities, release each retained
  allocation. Do not delete Coordinator Secrets, immutable grants, or binding
  records to bypass cleanup. Recreate a whole disposable environment when a
  deliberate unrecoverable fault prevents a subsequent clean run.
- Run unknown-operation tests last or on dedicated capacity. Unknown/pending
  journal entries, missing projection/journal evidence, and unresolved accepted
  mutations must block teardown, Machine finalizer removal, and reuse forever
  absent qualified recovery evidence. A timed-out proxy or restarted gateway is
  not such evidence. Do not restart a new session on the same occupied asset to
  escape that barrier.

## Validation and remaining gaps

```sh
go test ./cmd/fleet-controller ./cmd/fleet-session-fixture ./internal/staging/sessionfixture ./internal/controller
go test -race ./cmd/fleet-controller ./internal/staging/sessionfixture ./internal/controller
kustomize build config/manager >/dev/null
bash hack/validate-doc-links.sh
git diff --check
```

Tests cover actual reconciler admission, principal and object lifetime rejection,
grant-only denial, release blocking, unknown teardown rejection, next-epoch reuse,
and production/default wiring. Lifecycle deadlines are canonicalized to the
Kubernetes timestamp precision before acquisition so returned authority matches
the exact persisted binding even before a second API round trip.

Full Kind/Karmada/libvirt acceptance belongs to the E2E harness; it is not proved
by these portable tests. Production caller-preserving authorization, trusted
observations, provider-cleanup qualification and multi-executor fencing remain
unimplemented. Synthetic grants must never be used with real customer assets.
