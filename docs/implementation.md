# Fleet implementation status

Status: public API foundation, standalone synthetic provider simulator,
same-cluster allocation/installation lifecycle library, and an unwired NSO
network lifecycle adapter implemented. A running controller manager,
caller-preserving Milo authorizer, and real providers are not implemented.

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
the image catalog authority, revision, and image decision. Image references must
resolve to canonical lowercase SHA-256 digests. Stable operation and result
identities make response-loss recovery an operation lookup rather than a second
effect. Public claim status is not used as predecessor authority.

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

- The lifecycle package is not wired to a manager, informer watches, status
  projection, or finalizers yet.
- Cross-project class sources and bootstrap secret retention are rejected rather
  than partially executed. The network adapter is implemented but deliberately
  unwired until production location, authorization, and object-guard owners are
  available.
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
