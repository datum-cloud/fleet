# Registration activation gate

[Stage-1 contract](inventory-capacity-registration.md)

Inventory-backed capacity registration is not accepted for live use in any
shared environment. The code can be deployed in a non-admitting bootstrap, but
activation needs the release decision, ordered installation and caller
acceptance below. Allocation and provisioning remain outside this work.
Environment-specific evidence and wiring belong in the deploying repository,
not here.

## Release gate

The ledger is a 650,000-byte mutable tail plus at most 16 immutable sealed
segments. Each operator-signed `expand` seals the current tail losslessly into
one segment of up to 750,000 bytes and empties the tail. Nothing is released or
compacted. A full tail denies growing writes until expansion. Once all 16
segments are sealed, expansion is refused and growth stops until a separately
reviewed storage migration. The [storage runbook](registration-storage-rollout.md)
has the exact budgets and alert thresholds.

That bounds storage, not the number of operations over time. Even unregistered
Device creates and deletes permanently retain incarnations and aliases, so
expansion postpones the stop rather than removing it. A finite successful smoke
test cannot establish safe ongoing operation. PDBs and ledger metrics only
report how close the stop is.

Release is not implemented. Before activation on a shared authority, a reviewed
decision must specify:

1. Which authority can reconcile each exact operation and lifetime, with current
   authorization, immutable evidence and an audited CAS transition.
2. How pending API-server writes and retries are fenced before deciding an absent
   object cannot still appear. A timeout, a missing object, `Recorded`, or a
   deleted Project is not proof of release.
3. How aliases, tombstones, unknown outcomes and occupied capacity stay retained
   across crashes, partitions and any migration. Capacity availability must not
   follow ledger compaction or relocation.
4. A byte-budget and headroom model that includes failed requests, new
   incarnations and alias growth, plus a stop and escalation path before
   exhaustion. An external checklist alone is not an enforced growth bound.
5. Concurrency, authorization, restart and uncertain-write tests against real
   API-server storage. No manual Secret edits, age-based expiry, delete and
   recreate bootstrap, or arbitrary release command is permitted.

Bounded acceptance before that decision needs a **separate disposable Milo
authority and host ledger**, synthetic identities only, a bounded attempt and
byte budget, and no real-inventory routes or shared synthetic service. Keep that
authority's ledger for the whole experiment. Destroying an isolated test
environment is not a release mechanism for shared custody, and success there
does not show that a shared provider project is integrated.

## Installation order

Do not enable a new `failurePolicy: Fail` webhook on occupied storage before its
endpoint is proven. Do not relax an installed Fail webhook to work around a
missing backend.

1. Render registration from a host root that keeps existing synthetic consumers
   unchanged. The synthetic overlay excludes registration on purpose.
2. Install the retention policies and run their negative tests.
3. Provision separate root and project credentials (never a `system:masters`
   grant), the serving certificate and SAN, the API-server client certificate
   and CA, and the `fleet-registration-control` ConfigMap with `public.pem`.
   Restrict who can write that ConfigMap; it is the signing key's trust root.
4. Deploy a pinned image digest. The process listens in a non-admitting
   bootstrap that denies every admission. Prove the route with `/transportz`,
   which returns 204 on the same mTLS listener without the ledger or webhook.
5. Create the empty ledger root once. Abort on an existing ledger and validate
   it rather than replacing it.
6. Wire the webhook `caBundle` and the API-server admission client, verify the
   Fail rules, and read `/status`.
7. Sign `activate` offline and publish it. Activation pins the digest of the
   installed webhook fence. `/activationz` returns 503 until activation and its
   dependencies verify, and every admission still runs full fence verification.

The readiness probe is TCP only, so a Ready pod may still be non-admitting.
Gate UI activation on `/activationz` or `fleet_registration_activation_verified`,
not on pod readiness.

## Caller acceptance

Still required after activation: real staff proxy identity, trusted Project
extras, named Device read denial, create permission denial, direct
update/status/delete rejection, duplicate contention, TLS client rejection,
unchanged inventory and membership baselines, and Unknown recovery. Exercise
outage isolation only on disposable API and storage. Never scale down a shared
service or register real assets as smoke tests. UI deployment alone is not
acceptance.

## Availability safeguards

The service has a CAS-exhaustion counter, ledger gauges, a PDB, rolling-update
limits and preferred node separation, while the synthetic overlay keeps all
registration resources out. `/metrics` is served by the same `Transport()` mux
as `/status`, so it has the same API-server mTLS boundary and no separate
listener. The gauges are listed in the
[storage runbook](registration-storage-rollout.md#enforced-budgets). The PDB and
`maxUnavailable: 0` count TCP-ready pods, which may be non-admitting during
bootstrap or after a fence change, so they limit voluntary disruption only.
During a rollout, confirm `fleet_registration_activation_verified` is 1 on each
new pod before letting old pods drain; the kubelet cannot check it because the
endpoint needs a client certificate.
Passing local tests does not satisfy the acceptance gates above.
