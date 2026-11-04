# Fleet implementation status

Status: public API foundation, standalone synthetic provider simulator,
same-cluster allocation/installation lifecycle library, unwired NSO network
lifecycle adapter, unwired Milo tenancy/quota authority contracts, and an
event-only manager wiring scaffold implemented. A packaged standalone synthetic
smoke scenario exercises local-scope contention, install recovery, cleanup, and
epoch reuse. Production caller-preserving
admission, replayable Milo event adapters, and real providers are not implemented.

## Standalone synthetic smoke environment

`cmd/fleet-standalone-smoke` is a test-only, phase-oriented harness around the
implemented lifecycle coordinator. The packaged fixture uses two Kubernetes
namespaces as project stand-ins and one synthetic `us-central-1` machine with 24
cores and 128Gi of memory. The allocate phase retains custody before adding the
Fleet finalizer, binds one claim, and leaves the competing claim pending. The
install phase admits one exact digest-pinned operation through the persistent
simulator and is idempotent across worker and simulator restart. The release
phase verifies cleanup before finalizer removal and advances the retained asset
epoch before the waiting claim binds.

The harness is the designated writer for one retained synthetic Machine evidence
record and public fixture status. The evidence pins the Machine UID, configured
location, hardware, and observation time; it is not refreshed across phases.
Both contenders pass capacity preflight against the same retained
`Available=True` evidence before private custody selects the sole winner. The
lifecycle coordinator remains the writer of custody, asset,
and operation records. It does not patch status from shell, delete private
records, strip finalizers, or use public status as authority. The simulator runs
under an explicit non-root UID/GID with a writable `fsGroup` PVC and group-readable
runtime-only bearer Secret; its admin listener is off. Docker bases are digest
pinned, and the smoke script derives a source-specific local image tag and
verifies the running workloads use it with pulls disabled. That identity commits
to HEAD, its tree, the tracked diff, and untracked file contents; a CI regression
test proves distinct clean commits produce distinct IDs.

This is not the proposal acceptance environment. Namespace stand-ins do not
model Milo project APIs, caller identity, IAM, entitlement, quota, purge, or
same-named `default` namespaces. No interface is requested, so network and
address output is blank and no NSO/IPAM/Galactic behavior is claimed. See
[`docs/standalone-smoke.md`](standalone-smoke.md) for the runnable scenario.

## Event-only manager scaffold

`internal/manager` defines project-lifetime work keys, exact reverse indexes,
semantic predicates, dependency event mappers, thin claim/installation/projector
reconciler surfaces, a replayable external-event contract, and a one-shot
freshness deadline scheduler. Queue keys include the Milo authority ID, root
Project UID, namespace, name, and object UID. Equal names or UIDs in separate
project lifetimes cannot share work.

The controller-runtime cache uses an explicitly non-nil zero `SyncPeriod`, both
globally and for its private-Secret cache. Dependency mappers use exact field
indexes; private indexes consume only typed metadata exposed after the lifecycle
or Milo record owner validates the complete record and deterministic Secret
identity. Unknown or nested JSON lookalike fields have no routing meaning. The
manager's private-record predicate treats the Secret type and complete label set
as part of that trusted envelope, so a valid record that is relabelled or has
its type substituted is immediately reconciled into quarantine rather than
being hidden as a data-no-op update. The
mappers do not perform unqualified application Lists. Informer initial Add
events route retained private records after restart, including when the consumer
object is gone. Public status-only updates, including status written by an
external actor, do not feed the lifecycle coordinator; external facts that must
drive lifecycle work require an owned dependency resource and route.
Claim and Installation spec generations, labels, annotations, finalizers, and
deletion changes do feed it. Provider observations belong on dependency
resources: semantic Machine status changes use the dependency route rather than
turning public claim status into authority. Freshness expiry uses a replaceable
one-shot deadline, not a periodic resync.
Dependency events enter a typed route workqueue before reverse-index lookup, so
a cache read or List failure is retried as the same source event instead of being
mistaken for an empty target set. Every direct and routed handler also verifies
the configured Project lifetime through an injected trusted verifier.

Every dependency route now retains the source object's UID and exact routing
snapshot. Non-delete work checks that UID against the current named object; if a
queued name has been recreated, the old route is discarded and the replacement's
Add event owns its new lifetime. Delete/tombstone work instead resolves the
retained old-UID edges from its snapshot even if same-name recreation occurs
before reconciliation. Update events enqueue the deduplicated union of old and
new snapshots, ensuring both lost and gained selector or label relationships are
reconciled. Missing
project wiring is returned as an error so controller-runtime retains the exact
queue item under its bounded rate limiter. It is never converted into successful
pending work and silently dropped.

Claim work and Installation work each have one keyed lifecycle owner shared by
their direct, dependency-route, and deadline controllers. For an exact
ProjectRequest, that owner serializes the coordinator before the projector. The
coordinator alone may schedule or cancel a one-shot evidence deadline; expiry is
fed back through a typed controller source to the same owner. Projectors cannot
schedule work, and no separate projector controller can race the coordinator.
Both schedulers are manager-owned runnables: cancellation atomically stops and
drains timers, waits for any in-flight emission, and prevents post-stop work.
Informer initial Add processing reconstructs still-relevant deadlines after a
fresh process starts.

The setup watches the implemented Fleet resources, private records, NSO Network,
NetworkInterfaceClaim and NetworkInterface resources, Milo quota claims, and the
reviewed ServiceEntitlement shape. External IAM, project-lifecycle, catalog, and
executor sources must provide durable cursor/replay or materialize watched state;
a lossy channel is not sufficient. Business pending states wait for their next
dependency event and do not return a timed requeue.

External replay persists the entire returned batch before committing the
source-provided next checkpoint; event sequence identities are never treated as
cursors. Empty batches may still advance a source checkpoint. Watched deliveries
likewise carry an explicit post-event checkpoint. A crash after any persistence
but before the batch commit replays the same idempotent inbox writes. Pending
inbox work remains replayable until a consumer durably processes it and
acknowledges its receipt. This increment implements and tests those runner and
drain contracts; the production durable inbox, consumer, and live stream adapters
remain integration-owner responsibilities.

`cmd/fleet-manager` validates an exact project lifetime, starts the event graph,
and reports the integration readiness check as failed. Its handlers deliberately
perform no lifecycle or provider effects until trusted create-time
caller/finalizer admission and live, replayable Milo adapters are supplied. A
watch controller cannot recover the original `user.Info` or atomically capture
it after object creation, so this increment does not fabricate that authority
with annotations or controller credentials.

There is still no production manager Deployment. When one is introduced, its
service account must receive Secret permissions only through a namespaced Role
for the configured private-record namespace; cache scoping is not an
authorization boundary. Shipping that RBAC without the caller-preserving
admission, verifier, and replay integrations would create an inert privileged
workload and is deliberately deferred.

## Standalone lifecycle library

`internal/controller` implements the recovery-critical core without installing a
manager or watches. Callers first create retained claim custody before adding a
claim finalizer, retaining the namespace lifetime plus a trusted caller-supplied
principal UID and admission decision ID; the library never manufactures an
authorization result. Allocation requires that exact finalizer and authority and
rejects deleting claims.
Allocation then selects only current, healthy, available machines
with fresh externally supplied evidence. It atomically reserves each physical
asset through deterministic private Secrets and retains `Preparing`, `Committed`,
`Releasing`, and `Released` authority. Released records are not deleted; a later
binding advances the provider epoch and receives new binding and fence tokens.
The claim-level `Selecting` record retains the complete proposed asset authority
before creation, allowing deletion to materialize and tombstone the proposal so
a delayed allocator cannot resurrect it.

An independent custody record distinguishes a claim proven never bound from a
claim whose sole asset authority is unexpectedly missing. Release removes a
finalizer only after an unbound proof or retained cleanup completion. Corrupt,
duplicate, or cross-lifetime deterministic records fail closed.

Installation admission retains exact inputs and a private predecessor before the
provider effect, including the initiating principal and installation decision plus
the image catalog authority, revision, and image decision. The operation also
retains the installation and claim namespace, name, and UID, so an informer
initial Add can route unfinished work after restart even when the public
Installation has been deleted. This is installation-record schema v2. A v1
operation is upgraded atomically only when the exact public Installation and
claim request is still available to supply and verify the names omitted by v1;
otherwise it is quarantined from event routing and effects rather than guessed
or treated as ready. Image references must
resolve to canonical lowercase SHA-256 digests. Stable operation and result
identities include the authority/project claim scope, so equal claim UIDs from
independent project APIs cannot share an operation record. Installation custody
and asset lookup use that same scope; standalone requests derive an explicit
local scope from the Namespace UID. Response-loss recovery is therefore an exact
operation lookup rather than a second effect. Public claim status is not used as
predecessor authority. Before image resolution, operation creation, or a provider
effect, installation also requires Bound custody whose asset, claim identity,
scope, consumer/provider Projects, authority record, and external hold exactly
match the selected retained asset.

`internal/executor` is the typed bearer-authenticated client for the simulator's
runtime surface. Transport failures, unavailable responses, malformed terminal
results, and lost responses are classified as unknown outcomes for lookup-based
recovery. Bearer HTTP remains a test-only transport, not a production identity
contract.

The private namespace is a security boundary: the Fleet service account must be
its exclusive writer, and the `fleet-claim-`, `fleet-asset-`, and
`fleet-install-` name prefixes are reserved. Provider health, availability, and
observation timestamps supplied to the library must come from a trusted,
authenticated provider-status path; this increment does not establish that path.

## Network lifecycle adapter

`internal/network` defines an NSO-independent lifecycle contract. Allocation
preflights every requested interface before hardware selection, requiring exact
namespace and location lifetime authority plus a caller-preserving network-use
decision. The accepted Network UIDs, normalized request, decisions, and request
digest are retained in the private asset record before any NSO effect.
Fleet validates NSO's attachment shape before preflight and physical selection:
families are unique `IPv4`/`IPv6` values with at most two entries, and address
classes are unique with at most four entries. The Fleet CRD carries the same
cardinality constraints where OpenAPI can express them.

`internal/network/nso` implements that contract against the proposal's reviewed
NSO commit `d7fdc43f852450c447d4b7bb44b564c970582b1a`. It creates deterministic
NetworkInterfaceClaims only after binding and rejects name, UID, protected
metadata, or normalized-spec conflicts. A separate durable `ObjectGuard` must
prove Fleet reserved a claim name before an existing object can be recovered;
object annotations alone are not provenance. There is intentionally no default
authorizer or object guard.
The guard binds the created Kubernetes UID, so deleting and recreating the same
name with copied annotations or token is rejected. Cluster admission must reserve
Fleet's metadata keys and deterministic claim-name prefix against non-Fleet
writers. Guarded create/recovery and UID binding must be serialized atomically
against guard closure; a separate pre-create check is insufficient. Immediately before creation, production wiring must also verify the
retained Network UID and generation are currently Ready and every retained
address-class name still has the exact authorized lifetime UID.

The coordinator retains exact claim and interface UIDs plus current-generation
`Allocated`, `Prepared`, and `Programmed` observations. Installation admission
requires fresh `Allocated` and `Prepared` evidence and copies the exact network
revision and inputs into the private operation record. Public projection is a
pure helper that publishes only Network names, MTU, addresses, and normalized
conditions only while the observation is within the configured evidence age;
stale evidence projects `NetworkReady=Unknown`. Internal UIDs, NSO claim names,
and raw provider messages remain private.

Create intent for every interface is durably returned before any NSO create.
Release first durably admits an exact per-interface delete vector without effects,
then retries execute and verify individual deletes. Provider cleanup and finalizer release remain blocked while
network deletion, retained-interface detachment, or outcome evidence is pending
or unknown. Object absence counts only after the private record proves deletion
was admitted for the exact retained UID. `Retain` additionally requires the
same retained interface UID to report `Available` with no claim reference.
For `Retain`, an absent claim completes only when the closed guard proves the
claim was never created, or when an exact interface name and UID retained before
deletion later reports `Available` with no claim. A created claim whose binding
was never observed remains pending; a nil reference from one claim snapshot is
not durable proof that NSO could not bind it before deletion.

This package has no manager, watches, Milo integration, or deployment wiring.
Production wiring must supply a location-scoped client, caller-preserving
authorizer, and durable object guard; otherwise networked allocations fail
closed before selection.

## Milo authority contracts

`internal/milo` composes the lifecycle library with internal Milo project,
source-use, entitlement, quota, admission-fence, and release-custody contracts.
It is a library and test-fake integration only; it does not register a manager,
watches, webhooks, deployment, or acceptance environment.

Project identity retains the configured Milo authority ID, route name, and root
Project UID. The authority ID and root UID identify the lifetime; the route name
only selects a control plane. Preparation verifies both consumer and provider
lifetimes, then retains consumer Namespace UID, claim UID, local and canonical
class/profile UIDs, canonical class revision and content digest, immutable
profile requirements, publisher revision, initiating principal UID, and the
caller-preserving source decision in a private service-controlled Secret. The
lifecycle custody record links that protected authority record before later
work. Custody names and claim lookups include the authority ID, consumer Project
UID, and claim UID; standalone use has an explicit local scope keyed by the
Namespace UID. Equal claim UIDs from independent project API servers therefore
cannot occupy the same private record. Provider asset identity includes the Milo
authority and provider Project UID, preventing reuse across project lifetimes.

The adapter checks the exact retained claim, class, profile, location authority
(name, UID, and revision), revision, and content before capacity, quota,
fencing, or provider work. The source-use decision and admission commit bind
that complete class/profile/location combination. Current Machine inventory
does not carry separately authoritative placement evidence, so proving that a
selected Machine is physically in the retained location remains an upstream API
and provider-authority gap. The adapter accepts the
reviewed Milo v0.32.5 ServiceEntitlement evidence shape: Active phase plus a
current Ready condition on the exact entitlement UID. That API does not publish
the proposal's separate suspension evidence; ordering suspension against binding
remains an upstream admission-fence requirement rather than an invented local
condition.

Deleting claims and claims missing Fleet's protection finalizer are rejected
before quota acquire, admission fencing, or any provider effect.

Quota preparation durably records a deterministic intent before creating or
recovering one exact Milo ResourceClaim for
`fleet.datumapis.com/machines`. Grant evidence requires the exact object UID,
request, consumer and resource references, and current status
`observedGeneration`. ResourceClaim deletion or `NotFound` is never
quota-settlement proof. Allocation requires an injected protected custodian
before requesting quota. Acquire, intent recovery/no-hold closure, and settlement
all pass through that custodian under the exact Project, authority record, and
intent lifetime. A no-hold receipt closes that intent against a later acquire;
the adapter never calls the ResourceClaim manager directly. A full
admission-commit digest binds the retained
source decision, exact entitlement lifetime, and exact quota hold UID,
generation, and evidence to the fence receipt. Sequential checks are not
presented as ordering concurrent invalidation against binding.

Cleanup advances assets only to `CleanupVerified` while an external hold exists.
Fleet persists an exact custodian settlement receipt before releasing lifecycle
custody, and only then advances the asset to `Released` and permits finalizer
removal. Any retained hold is first installed into core custody even when a
crash occurred before that write. Quota recovery and settlement use the exact
retained consumer Project lifetime through the service-owned custodian rather
than resolving a possibly deleted or route-name-reused consumer API endpoint.
Retried allocation and release resume persisted fence, binding, intent, and
settlement states before considering new work. Persisted fence receipts are
revalidated against the complete retained commit on every decode. Unknown
create, release, or project outcomes retain custody and remain unknown.

The tests verify source and lifetime substitution rejection before effects,
concurrent authority-record creation, both admission-fence orderings, stale or
corrupt persisted fence receipt rejection, lost quota-create recovery, a paused
acquire racing no-hold closure, hold-write crash recovery, pending-hold deletion,
binding recovery after a lost response, retry after a crash between core hold
clear and authority reset, cross-project claim-UID collisions,
cleanup-before-quota ordering, settlement-receipt replay, deleted or
route-name-reused consumer-project cleanup, malformed authority state rejection,
and delayed allocation or settlement racing completed release. These are
contract tests with fakes, not evidence of
a working Milo production integration.

## Persistent provider simulator

`cmd/fleet-simulator` is a test-only process for exercising provider-operation
recovery before a real hardware adapter exists. It supports synchronous
`install`, `power`, and `cleanup` operations plus operation lookup. It never
contacts, enrolls, or authorizes real hardware.

The simulator writes one versioned JSON state file. Each mutation writes and
syncs a temporary file, atomically renames it over the retained state, and syncs
the parent directory. Operation IDs are idempotency keys: replaying the exact
request returns its retained result, while different retained input conflicts.
Every effect carries an asset ID, binding ID, monotonically increasing epoch,
and fencing token. Cleanup closes that epoch so delayed work from the released
binding cannot reactivate the asset; a later binding must advance the epoch.

The `lost-response` fault persists the completed effect and consumes the fault in
the same state write before returning an unavailable response. A caller recovers
the retained result by operation ID instead of submitting new authority. A
failure after the state-file rename remains queryable in memory and is reported
as an unknown response outcome rather than rolling the process back behind the
visible file.

The runtime and fault-injection surfaces use separate bearer credentials. The
admin listener is disabled by default, and the runtime listener defaults to
loopback. Runtime responses omit binding IDs, fencing tokens, and exact inputs.
Request bodies, identifiers, headers, and server timeouts are bounded. These
controls limit the local test surface; bearer authentication over plain HTTP is
not a production executor identity protocol.

The persisted schema and HTTP endpoints are internal alpha interfaces. No
compatibility promise exists yet. The process fails closed if retained state is
corrupt or uses an unsupported schema version.

## Build, test, and run

Run the implemented checks from the repository root:

```sh
go test ./...
go test -race ./...
go vet ./...
```

For an isolated local run, create a synthetic credential file and keep the
default loopback listener:

```sh
runtime_token_file=$(mktemp)
chmod 600 "$runtime_token_file"
openssl rand -hex 32 >"$runtime_token_file"
go run ./cmd/fleet-simulator \
  --runtime-token-file="$runtime_token_file" \
  --state-file=/tmp/fleet-simulator-state.json
```

Fault injection and full retained-state inspection are disabled by default. To
enable them locally, create a second credential with a different value and add:

```text
--admin-listen-address=127.0.0.1:8081
--admin-token-file=/path/to/distinct-admin-token
```

The process refuses to start if the runtime and admin credential values match.
Remove the synthetic token and state files when the local run is complete.

## Explicit gaps

### Milo acceptance harness

`test/acceptance/milo` implements a non-effectful acceptance boundary. It
requires exact Project lifetimes, Fleet and Milo revisions, digest-pinned
manager and simulator images, and explicit credential paths. Its evidence
encoder allow-lists non-secret environment identity and writes restricted,
atomic JSON and JUnit artifacts through a fresh staged three-file allowlist.
The config boundary walks from a held root descriptor with component-by-component
no-follow `openat`, requires an owner-only final file, and performs a bounded read before
JSON decoding. Phase reasons
and blockers use closed reviewed code vocabularies rather than external error
text. The staged summary must validate as the closed Result schema, and the
environment and JUnit files must exactly match canonical projections from it.
Verification retains the root and safely traversed parent descriptor identities, opens the evidence
basename with no-follow `openat`, and opens every allowlisted file once with
no-follow `openat`, validates bounded reads from the held descriptor set, then
securely rewalks and rechecks the complete absolute ancestor chain, the
parent-relative directory identity, and every path against the held
inode, mode, and size.
Invalid configuration cannot copy caller fields into evidence. A central result
validator enforces a passed prefix, exactly one outcome-matching terminal phase,
and a not-run suffix; invalid configuration is exactly the configuration phase.
Workflow logs emit only reviewed failure codes, never raw execution errors.
The workflow uses an absent run-ID/run-attempt-specific output directory so a
failed fresh write cannot upload artifacts left by an earlier run.
Credential paths are canonicalized, and
the mandatory pre-access check rejects symlinks, non-regular files, lexical
aliases, and files that share operating-system identity.
The credential boundary returns already-open no-follow descriptors and rechecks
their identity and owner-only permissions; later consumers must not reopen
paths. Evidence staging rejects symlinks in every existing parent component.
This is sufficient for the current non-concurrent blocked harness; a concurrent
live writer will require held directory descriptors and no-follow `openat`
traversal.

The current live command always stops after structural validation with
`Blocked` because the production protocol probes and live scenario executor do
not exist. It does not load a kubeconfig, contact a cluster, or mutate an API.
The protected manual workflow therefore demonstrates fail-closed behavior, not
Milo acceptance. Offline validation and tests cannot satisfy the proposal's live
acceptance inventory.

- The event manager exposes claim, installation, and sole-writer projector
  surfaces, but production lifecycle handlers and public status writes remain
  unwired. Trusted create-time caller capture and protected finalizer admission
  cannot be reconstructed from informer events.
- Live Milo project/IAM/catalog/quota event adapters and asynchronous executor
  completion sources are not implemented. They must support durable replay;
  polling is intentionally not a fallback.
- Cross-project class sources and bootstrap secret retention are rejected rather
  than partially executed by the standalone coordinator. The Milo adapter can
  resolve a canonical source only when injected with trusted source authority,
  fence, and custody implementations. The network adapter remains deliberately
  unwired until production location, authorization, and object-guard owners are
  available.
- Milo v0.32.5 supplies ResourceClaim evidence but no service-owned quota custody
  anchor or explicit release acknowledgment. It also does not supply Fleet's
  caller-preserving source/provenance receipt, atomic invalidation-versus-bind
  fence, consumer-project deletion recovery, or provider-project purge guard.
  Production cross-project allocation therefore fails closed; only test fakes
  implement these interfaces in this increment.
- `LocalDigestImageResolver` is test-only. Production image authority,
  namespace authorization, catalog lifetime identity, and digest resolution are
  not implemented.
- No container image or Kubernetes deployment is delivered in this increment.
- There is no mTLS workload identity, target-level authorization, or production
  provider credential protocol.
- Operations complete synchronously. Delayed completion, partial effects,
  cancellation boundaries, synchronization barriers, and worker takeover remain
  unimplemented.
- The state file is intended for a single simulator process. Replication,
  migration, backup, and upgrade/downgrade compatibility are not implemented.
