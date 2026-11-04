# Standalone synthetic smoke environment

This environment exercises Fleet's implemented local-scope lifecycle library
against the persistent provider simulator. It is a development smoke test, not
the Milo acceptance suite and not hardware qualification.

`fleet-project-a` and `fleet-project-b` are Kubernetes namespaces used only as
project stand-ins. They do not model Milo routing, IAM, entitlement, quota,
project deletion, or equal `default` namespaces in separate project API servers.
The fixture supplies synthetic admission and retains one exact Machine evidence
record with its Machine UID, location, hardware, and observation time. Later
phases fail closed if that record is missing, stale, or substituted. The
production `fleet-manager` remains fail-closed and performs no effects.

The scenario has one `us-central-1` Machine with 24 physical CPU cores and
128Gi of memory. Both consumers request it. Project A binds first while project
B remains pending. Project A installs a digest-pinned synthetic Debian image.
The script restarts the simulator and reruns the install phase to recover the
same retained operation. It then releases project A, verifies cleanup, and lets
project B bind at a later executor epoch.

No network interface is requested. The `NETWORK` and `ADDRESS` columns are
therefore intentionally blank; this environment does not deploy or impersonate
NSO, IPAM, or Galactic programming.

## Run with kind and Colima

Create or select a dedicated kind cluster named `fleet-core`, then run:

```sh
KUBECTL_CONTEXT=kind-fleet-core KIND_CLUSTER=fleet-core ./hack/standalone-smoke.sh
```

The script refuses a cluster that already has Fleet APIs (including a prior
standalone run), fixture namespaces, or a `fleet-manager`/`fleet-milo-manager`
deployment. Use a fresh dedicated cluster for every run. This prevents another
controller, retained epoch, or old claim from making the evidence ambiguous.
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
It then applies the CRDs and fixture and uses `kubectl wait`/rollout watches for
phase completion. It never patches status, removes finalizers, or writes
lifecycle records itself. The
phase Job is the designated synthetic fixture and status writer; the lifecycle
library remains the only writer of custody, asset, and operation records.

After the allocation phase, expected output has one ready and one pending claim:

```text
NAMESPACE         NAME         PHASE     LOCATION       CPU   MEMORY   NETWORK   ADDRESS   REVISION
fleet-project-a   allocation   Ready     us-central-1   24    128Gi
fleet-project-b   allocation   Pending   us-central-1
```

After release, only project B remains and reports the same hardware. The private
records remain in `fleet-system-private`; release does not erase recovery or
epoch history.

## Security and lifecycle limits

The simulator has a dedicated runtime token, a persistent volume, no admin
listener, an explicit non-root UID/GID, a read-only root filesystem, and no
Kubernetes API credentials. Its pod `fsGroup` makes only the state volume and
group-readable token usable by that identity. Both Dockerfile bases are pinned
by digest. Only the smoke service account can write private records; its public
access is restricted to the named fixture objects. The script checks every
sensitive Secret verb, named Secret reads, and out-of-scope fixture access.

The synthetic phase worker deliberately does not reconstruct or claim an end
user identity. Production still requires create-time caller capture, protected
admission/finalizers, real project-lifetime routing, source-use authorization,
entitlement, quota custody, an invalidation fence, and replayable Milo events.
Until those owners are wired, this scenario proves only local lifecycle and
simulator recovery behavior.
