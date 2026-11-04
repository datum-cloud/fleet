# Fleet implementation status

Status: public API foundation, standalone synthetic provider simulator, and
same-cluster allocation/installation lifecycle library implemented. A running
controller manager, networking integration, and real providers are not implemented.

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
go test -race ./internal/controller ./internal/executor
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
- Cross-project class sources, network attachments/IPAM, and bootstrap secret
  retention are rejected rather than partially executed.
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
