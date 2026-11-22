# Registration storage expansion and non-admitting bootstrap

[Stage-1 contract](inventory-capacity-registration.md) · [Packaging](../../config/README.md)

## Delivery boundary

Implemented against feature revision `32c3c200b7afd50a43ae705609f8f5fad0c63d40`.
This change is independent of pending PR144 (`dc0f872977ecf817557b96fa82d53b853110a198`),
whose staging integration document was read in full. It does not replace that
PR's availability work or evidence; reconcile overlapping deployment/ledger edits
when combining them. **Parent independent adversarial review is required.**

The two Fleet prerequisites are addressed by bounded, lossless expansion (not
alias release) and transport-first, deny-only startup. This is not live acceptance.
No live mutation, grant, deployment, allocation or provider effect is authorized
by this change. The reported mixed live pairing (synthetic host without the service,
production Milo Fail webhook without caBundle) must not be repaired by relaxing
admission or changing host overlays indiscriminately. Existing Enabled capacity
and synthetic consumers are not disposable acceptance fixtures.

## Single-CAS storage protocol

The original `fleet-capacity-registration-v1` Secret and UID remain the sole CAS
root. Version 2 adds `routing.json` and `tail.json`. Immutable Secrets named
`fleet-registration-segment-<root-UID-digest-prefix>-<epoch>` retain sealed tails.
Every read verifies segment name, UID, immutability, deletion state, payload digest
and authority/provider scope. Missing/replaced/corrupt/unreadable storage is never
an empty partition. The same root still serializes all aliases and provider projects.

Segments are ordered overlays, not independently admitting hash shards. Groups
are keyed by admission operation UID for registrations and Project route/Device UID
for Device fences. Only changed groups are copied into the mutable tail, whole.
Legacy multiple-version groups are sealed without reinterpretation. Existing
authoritative update/coalescing rules alone supersede pending versions. The writer
also rejects dropping registrations, aliases, tombstones or known lifetime identity.
New Device fences retain Project UID, namespace name/UID and Device name; missing
legacy fields are not backfilled or proof of safe release. Creates and edits
require a live namespace. Deletes and finalizer-only updates do not, so a
terminating provider namespace can drain its Devices. They bind the terminating
namespace's UID, or the UID already recorded once the namespace is gone, and
still write a permanent tombstone. Registered Devices remain undeletable.

An operator signs `expand`, binding exact root UID, resourceVersion, epoch,
authority, sorted provider scope, change ID and reason. The service:

1. Verifies the pinned Ed25519 key and CAS-retains the request as pending before
   effects. First conversion replaces `ledger.json` with an incompatible authority
   sentinel, retaining its original bytes in `tail.json`. Old binaries reject it;
   old in-flight writes lose CAS. Host policy forbids reverting the v2 format.
2. Creates one deterministic immutable segment with the frozen tail, root UID and
   signed request. Every replica resumes the same name. AlreadyExists requires
   exact content and immutable lifetime checks, never overwrite permission.
3. Reads back the segment, then CAS-appends its name/UID/digest, advances epoch and
   empties the tail. Prior segments, activation and audit history remain retained.
4. Resumes admission under the same activation and full fence checks. Migration
   never changes `Unknown`, `Recorded`, tombstones, reservations or eligibility.

Crash/timeout before freeze has no authorized storage effect. After freeze, any
replica resumes retained authority even without the request file. A prepared
segment is associated with the pending operation, not garbage. A lost final CAS
response is reconciled from the root. No timeout/lease/age/absence cancels pending
work. Stale plans fail; inspect before re-signing. A public request admitted before
freeze may persist afterwards: its reservation was copied, so no claim of fencing
the API server's later storage commit is needed. A not-yet-reserved writer must win
the current root CAS or be denied/re-evaluated. No orphan alias is discarded.

This does not implement release, scope changes, arbitrary restore or custody
compaction. Rollbacks need a v2-aware binary. Never restore an old root alone;
root plus all segments are one retained recovery set. Administrators who remove
policies or restore arbitrary storage remain outside the guarantee.

## Enforced budgets

| Surface | Hard bound |
| --- | --- |
| Mutable tail | 650,000 encoded bytes; larger candidate denied before admission success |
| Legacy tail accepted for sealing | 750,000 bytes, including an already-full v1 ledger |
| Root data including routing/control | 850,000 bytes, below the 1 MiB Secret data limit |
| Sealed segments | 16 plus the mutable tail; expansion is operator-authorized, not infinite |
| Signed payload / mounted envelope | 8,192 / 16,384 bytes |
| Activation audit | Current activation plus seven previous signed activations, never pruned; the ninth activation fails (lifetime limit) |

The ordinary stop leaves 100 KB relative to the former ledger cap for safe sealing;
the root budget additionally accommodates routing/control. A segment is at most
750 KB plus its small control envelope. Failed creates, aliases, distinct Device
incarnations and failed registrations consume budget. Oversized requests cannot
consume transition reserve. Segment retries target one name, not orphan snapshots.
Maximum ledger payload is 16 × 750,000 + 650,000 bytes plus bounded control metadata.
This deliberately retains complete provider scans and the single-root bottleneck;
it is not a throughput qualification. Pod memory is bounded at 512 MiB.

The mTLS-only `/status` reports tail bytes, headroom, remaining segments, epoch,
pending state, root UID/RV, scope and verified fence digest, without reservation
contents or actor data. `/metrics` on the same listener exports the same verified
read as unlabeled gauges, plus the CAS-exhaustion counter:

| Metric | Meaning |
| --- | --- |
| `fleet_registration_ledger_readable` | 1 when the root and every segment read and verified; 0 means unknown, and the other ledger gauges are omitted |
| `fleet_registration_ledger_tail_bytes` | Encoded bytes in the mutable tail (the v1 `ledger.json` before first expansion) |
| `fleet_registration_ledger_tail_headroom_bytes` | 650,000 minus tail bytes |
| `fleet_registration_ledger_sealed_segments` | Sealed immutable segments, 0 to 16 |
| `fleet_registration_ledger_remaining_segments` | 16 minus sealed segments; at 0 `expand` is refused |
| `fleet_registration_ledger_epoch` | Routing epoch; advances once per completed expansion |
| `fleet_registration_ledger_expansion_pending` | 1 while a retained signed expansion is incomplete; admission is fenced |
| `fleet_registration_activation_verified` | 1 when the signed activation and installed fence verify; 0 is non-admitting |
| `fleet_registration_cas_retries_exhausted_total` | Admissions denied after six CAS conflicts |

Parent monitoring must alert when headroom falls below 100,000 bytes and before
only two segments remain. Every replica reads the same root, so aggregate with
`min` across replicas:

- `min(fleet_registration_ledger_tail_headroom_bytes) < 100000`: sign `expand`.
- `min(fleet_registration_ledger_remaining_segments) <= 3`: fires while three
  remain, before only two do. Escalate for the reviewed storage migration.
- `min(fleet_registration_ledger_readable) == 0`, or the series is absent: at
  least one replica cannot verify the ledger. Do not read missing gauges as
  headroom.
- `fleet_registration_ledger_expansion_pending == 1` for more than a few check
  intervals: inspect `/status`, the key and segment state.
- `fleet_registration_activation_verified == 0` after activation: registration
  is non-admitting; check the fence before signing anything.

Each scrape performs the full verified read, including every segment and fence
verification against the root API, so use a modest scrape interval. Each
replica serves one scrape at a time and answers a concurrent scrape with 503. Scraping
needs a client certificate trusted by the dedicated API-server client CA. That
certificate can also call admission, so give it only to a restricted scraper,
never to general monitoring. Sign expansion before exhaustion. A full v1
ledger reports `registration ledger is full; sign an expand control request`;
a full v2 tail with segments left reports `registration growth stopped`. Both
mean sign `expand` as in step 7 below. Once all 16 segments are sealed, writes
and `expand` report `registration segments exhausted`: expansion is refused, so
re-signing it cannot help. At that terminal bound, stop protected growth and
escalate for a separately reviewed storage migration; never increase caps or
delete/reset/release records manually.
Public inventory reads remain unaffected. Denied/timed-out private writes deny
admission even if reads succeed; uncertain commits stay retained.

## Transport versus activation

The process loads credentials, TLS material and the operator public key, then
listens without requiring the ledger or installed webhook. It denies **every**
admission in bootstrap. `/transportz` returns 204 on the same mTLS listener for
SAN/CA/routing verification. The pod TCP readiness probe intentionally supplies
Service endpoints during bootstrap; it is not the UI admission gate.
`/activationz` returns 503 until activation and current readable dependencies
verify. It is not proof of write permission, future availability or capacity
eligibility. Request headers/labels/annotations/extras cannot enable admission;
there is no bootstrap bypass flag or public control endpoint.

Signed `activate` also pins the digest of the installed, API-defaulted webhook
`webhooks` array, obtained from `/status` after `VerifyFence` succeeds. Activation
CAS and every admission retain full fence verification. Missing trust, changed
endpoint/CA, partial deployment, relaxed rules or degraded dependencies deny.
Approved trust changes require another exact signed activation; prior signatures
remain in bounded root audit history. The configured signing key is an operator
trust boundary, not caller authority; coordinate changes with pending signatures.
Global capacity fencing and privileged/root-context limitations are unchanged.

### Activation budget is a lifetime limit

The root keeps the current activation plus seven previous ones. The ninth
activation of a root fails with `activation audit budget exhausted`. Nothing
prunes or compacts this history, so the limit applies to the root's whole life
until a separately reviewed compaction migration exists. Every change to the
pinned fence digest needs a new activation and uses one slot. That includes
webhook CA rotation (the `caBundle` is part of the digest) and API-server drift
in defaulted webhook fields after an upgrade. Batch planned trust changes into
one activation where possible, and count the remaining slots before each one.
At the cap, admission stays on its current activation while that activation's
fence still verifies. If the fence has already changed, registration stays
non-admitting. Do not delete or edit `routing.json`, and do not create a new
root to reset the count. Escalate for the reviewed compaction migration.

### Rotating the operator signing key

The public key is read once at process startup from `public.pem`, and every
signature is verified against the key the process currently holds. A pending
expansion cannot be cancelled, and `ResumeExpansion` re-verifies the pending
signature against the current key. If the key rotates while an expansion is
pending, the expansion cannot complete and the root stays fenced. Rotate in
this order:

1. Fetch `/status` and confirm no expansion is pending. If one is, let it
   complete under the current key and confirm the epoch advanced. Do not sign
   a new `expand` until the rotation is finished.
2. Replace `public.pem` in `fleet-registration-control`, then restart every
   registration replica, because a running process keeps the key it loaded.
3. The retained activation was signed by the old key, so the restarted
   processes stay non-admitting. Sign a fresh `activate` with the new key
   (step 7 below). This uses one activation slot.

## Exact staged parent infrastructure contract

These steps require a separately approved infra PR; none authorizes live execution.

1. Preserve private baseline evidence. Resolve registration authority and complete
   provider scope separately from synthetic consumers. Do not narrow scope to
   conceal legacy capacity or reuse manager root grants. Capacity writes remain
   globally fenced, including outside `datum-cloud`.
2. Install retention policies before bootstrap or broader RBAC. The service gets
   get/create Secrets in the dedicated private namespace and update only on the
   root. Fail host admission restricts its creates to immutable segment names;
   it cannot create the root/arbitrary Secrets or update/delete segments. Prove
   negative cases and retain backups of **all** segments; never use GitOps to
   reconcile an empty root. Namespace/root deletion remains forbidden.
3. Provision separate root/project read-and-SAR identities, serving SAN/CA,
   dedicated webhook client certificate/CA, and `fleet-registration-control`
   ConfigMap in `fleet-system` with `public.pem` (Ed25519 PKIX PEM). Restrict
   ConfigMap/workload writers as operator trust authorities. No signing private
   key, kubeconfig or rendered secret-bearing configuration belongs in Git.
4. Pin the reviewed image and deploy registration before activating a new Fail
   webhook. With the already-broken live fence, leave protection installed.
   Verify `/transportz` from the API-server route with trusted client TLS, and
   reject missing/untrusted clients. No public write is a health probe; never
   remove/relax the existing fence to test routing.
5. Create the empty root once with exact authority and sorted projects in
   `ledger.json`, after retention acceptance. Abort on AlreadyExists and inspect
   rather than replace. Existing v1 roots activate/migrate in place with the same UID.
6. Wire Milo's dedicated webhook client and serving caBundle in environment-owned
   overlays. Verify complete Fail rules. Fetch `/status` over trusted transport
   and review the installed fence and root UID/RV/epoch/scope/fence digest.
7. Sign a `ControlRequest` JSON containing `action: "activate"`, `id`, `reason`,
   `authority`, `projects`, `rootUID`, `resourceVersion`, `epoch`, `fenceDigest`:

   ```sh
   go run ./cmd/fleet-registration-control --request="$PRIVATE_REQUEST" --key="$OFFLINE_KEY" > "$PRIVATE_SIGNED_REQUEST"
   ```

   The helper requires a PKCS8 Ed25519 private key. Only its signed envelope goes
   into ConfigMap `request.json`; the private key is never mounted. The process
   checks every 30 seconds. Reconcile through `/status` and `/activationz`, not
   ConfigMap update success. For expansion use `action: "expand"`; no fence
   digest is required, and expansion cannot activate a bootstrap root.
8. Keep UI registration gated until real staff proxy identity, named Device and
   create authorization denials, direct mutation denials, duplicate contention,
   lifetime/legacy behavior, Unknown recovery and unchanged baselines are accepted
   on intended Milo routes. Use disposable environments for outages/failures,
   never shared-service disruption or real-asset smoke tests. Only the parent
   can then report environment acceptance.

## Local verification

Tests exercise real Secret CAS/retention, bounded expansion, legacy integrity,
multi-replica migration, signature/scope/epoch spoofing, read-only RBAC and
persisted-but-timed-out writes. A real API request paused after admission fails
public persistence after migration; its aliases remain reserved and Unknown.
Five subprocess exits cover freeze, before/after segment persistence and
before/after root switch, recovered by new processes. Missing/replaced segments
fail closed. Existing identity, authorization, legacy and outage tests remain.

```sh
KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.25.2 use 1.37.0 -p path)" go test -race ./...
go vet ./...
go build ./...
kustomize build config/milo >/dev/null
kustomize build config/overlays/management-plane >/dev/null
./hack/validate-overlay-isolation.sh
./hack/test-overlay-isolation.sh
./hack/validate-doc-links.sh
git diff --check
```

No UI files change. These checks do not accept deployed Milo routing, credential
mounts, grants, GitOps ordering or real inventory. Disabled/Retain remains inert.
