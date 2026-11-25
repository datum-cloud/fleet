# Standalone synthetic smoke environment

[Operations guide](README.md) · [Troubleshooting](../troubleshooting/README.md)

This environment exercises Fleet's implemented allocation lifecycle on [Device
custody](../development/device-custody.md#cutover-increment-c) in one kind
cluster. A Deployment runs the custody coordinator, and the simulator is the
only site. It is a development smoke test, not the Milo acceptance suite and not
hardware qualification. Status: **implemented and verified** by
`task standalone:test`.

`fleet-project-a` and `fleet-project-b` are Kubernetes namespaces used only as
project stand-ins. They do not model Milo routing, IAM, entitlement, quota,
project deletion, or equal `default` namespaces in separate project API servers.
The fixture supplies synthetic admission. The capacity projector is not wired,
so the install and allocate phases write `Prepared`, `Healthy` and `Available`
on the capacity themselves. Custody, not those conditions, decides exclusivity.
The production `fleet-manager` remains fail-closed and performs no effects.

Each component has its own identity, and the phases run as the Metal provider:

| Identity | ServiceAccount | Writes |
| --- | --- | --- |
| Custody coordinator | `fleet-custody-coordinator` | Custody and operation status |
| Metal provider | `fleet-standalone-smoke` | MachineAllocations and DeviceInstallations |
| Release controller | `fleet-release-controller` | DeviceWipes |

A ValidatingAdmissionPolicy (`config/standalone/site.yaml`) requires each
operation's `spec.creator` to be the caller that creates it.

The scenario has one `us-central-1` Machine with 24 physical CPU cores and
128Gi of memory, on one synthetic Device, `synthetic-device-central-01`. Both
consumers request it. Custody must be `Verified` first. Project A allocates
first, so one `MachineAllocation` exists and custody names it. Project B waits
with `InsufficientCapacity`. Power comes only from the manager's power
projector, which reads custody and fixture discovery. Project A installs a
digest-pinned synthetic Debian image through a `DeviceInstallation`. The script
restarts the simulator and reruns the install phase to recover the same retained
operation. A separate `debian-12-reinstall` Installation then names `debian-12`
as its expected predecessor and replaces it on the same allocation. Another
simulator restart and retry recover that exact successor operation. Kubernetes
completion records alone cannot pass the restart check.

Project B remains pending throughout. The scenario then releases project A. The
release wipes the Device once through a `DeviceWipe`, under a fence above both
installations. Custody then names project B's new allocation, and project B
becomes `Ready`. Project A's allocation is `Released`.

An unknown site result keeps the Machine's readiness unknown and holds custody.
Neither a retry nor a release can expose that capacity before recovery.

No network interface is requested. The `NETWORK` and `ADDRESS` columns are
therefore intentionally blank; this environment does not deploy or impersonate
NSO, IPAM, or Galactic programming.

## Run with kind and Colima

For a complete disposable run, install Docker with a Linux container engine,
kind, kubectl, kustomize, Git, and Python 3, then run from the repository:

```sh
./hack/run-standalone-smoke.sh
# Or: task standalone:test
```

The wrapper creates a uniquely named kind cluster and a private temporary
kubeconfig, runs the scenario, and deletes only its own cluster. It does not
change your current Kubernetes context. Failure, timeout, and interrupt paths
also attempt cleanup; unverified cleanup is reported as failure with the exact
cluster name. Command output is withheld by the wrapper to avoid leaking local
credentials. The final phase label identifies where a failure occurred.
`FLEET_SMOKE_TIMEOUT_SECONDS` defaults to 1200 for the whole run and
`FLEET_SMOKE_CLEANUP_TIMEOUT_SECONDS` defaults to 120 for cleanup.

The `ci` workflow runs this same wrapper on `ubuntu-latest` for pull requests and
main pushes, using Docker and kind rather than self-hosted or nested-KVM runners.
That job exercises the synthetic control-plane lifecycle, not a real guest OS,
Milo production admission, or provider hardware qualification. Passing local
checks is not evidence that a particular GitHub Actions run passed.

To retain a cluster for local inspection, use the lower-level script instead.
Create or select a dedicated kind cluster named `fleet-core`, then run:

```sh
KUBECTL_CONTEXT=kind-fleet-core KIND_CLUSTER=fleet-core ./hack/standalone-smoke.sh
```

The script refuses a cluster that already has Fleet APIs (including a prior
standalone run), fixture namespaces, or a `fleet-manager`/`fleet-milo-manager`
deployment. Use a fresh dedicated cluster for every run. This prevents another
controller, retained custody, or old claim from making the evidence ambiguous.
It also refuses a context whose API server does not match `kind get kubeconfig`
for the requested cluster. A differently named context is accepted only with
`ALLOW_KIND_CONTEXT_ALIAS=true`, and must still target that exact kind cluster.

The script verifies that its kubeconfig context targets the named kind cluster,
derives a repository source identity from the committed HEAD and tree plus
tracked and untracked changes, and uses it as an immutable local tag. That
identity covers more than the Docker build context by design: the build context
itself is deny-all except for `go.mod`, `go.sum`, `cmd/`, and `internal/`, so Git
metadata, credentials, documentation, and manifests are never sent to the
builder. The script builds and loads both images and verifies each running pod
uses the exact source-specific tag with image pulling disabled. Distinct clean
commits therefore cannot silently reuse one local workload tag.
It applies the CRDs first and waits for establishment before applying fixtures,
then uses `kubectl wait`/rollout watches for
phase completion. It never patches status, removes finalizers, or writes
custody or operation records itself. The phase Job is the designated synthetic
fixture and status writer. The coordinator Deployment is the only writer of
custody and operation status.

After the allocation phase, expected output has one ready and one pending claim:

```text
NAMESPACE         NAME         PHASE     LOCATION       REQUESTED CPU   REQUESTED MEMORY   NETWORK   ADDRESS
fleet-project-a   allocation   Ready     us-central-1   24              128Gi
fleet-project-b   allocation   Pending   us-central-1   24              128Gi
```

Requested CPU and memory come from `status.requestedHardware`, which the runner
pins from the exact fixture profile for both claims, including the one still
waiting. `kubectl get machines -A -o wide` adds the profile and the allocated
CPU and memory, which stay blank for the pending claim.

After release, only project B remains and reports the same hardware. The
released allocation, the installations and the wipe remain in the provider
namespace (`fleet-provider`), so the fence history is kept:

```sh
kubectl -n fleet-provider get machineallocations,deviceinstallations,devicewipes,devicecustodies
```

## Security and lifecycle limits

The simulator site has a dedicated runtime token, a persistent volume for its
site store, no admin listener, an explicit non-root UID/GID, a read-only root
filesystem, and no Kubernetes API credentials beyond its fixture discovery. Its
pod `fsGroup` makes only the state volume and group-readable token usable by that
identity. Both Dockerfile bases are pinned by digest. The script checks that the
project stand-ins cannot read or write custody, operations or allocations and
cannot read the three token Secrets. It also checks that the Metal provider cannot
update custody or create wipes, that the release controller cannot create
installations, and that the smoke account's access to fixtures is limited to the
named objects.

The synthetic phase worker deliberately does not reconstruct or claim an end
user identity. Production still requires create-time caller capture, protected
admission/finalizers, real project-lifetime routing, source-use authorization,
entitlement, quota custody, an invalidation fence, and replayable Milo events.
Until those owners are wired, this scenario proves only local lifecycle and
simulator recovery behavior.
