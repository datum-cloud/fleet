# Registration staging integration: blocked rollout

[Stage-1 contract](inventory-capacity-registration.md)

## Decision and scope

Assessment on November 22, 2026: **not accepted for live registration**.
PR141 merge and publication are verified; endpoint trust, durable bootstrap,
caller acceptance and safe release are not. No inventory, capacity, membership,
admission, deployment or GitOps resource was changed during this assessment.
Allocation and provisioning remain outside this work.

All host reads used
`gke_datum-cloud-staging_us-east4_infrastructure-control-plane-staging` explicitly.
`datumctl whoami` confirmed `api.staging.env.datum.net` and
`datum-technology/datum-cloud`. No implicit gcloud context was used.

## Observed deployment chain

- Feature merge: `32c3c200b7afd50a43ae705609f8f5fad0c63d40`.
- Both Fleet OCI sources advertise that exact source revision. Applied bundle:
  `sha256:883c5064ca7f063f594fcea790b25c0489c88530680c1c023df343e3bffe8889`.
  The Flux downloaded archive digest is separately
  `sha256:561388ff384a47f09ca4ea3a4b0f05fe3640167e1e148989ab0398feaa9224b0`;
  it is not the OCI manifest digest.
- `fleet-system/fleet-host` is Ready but renders `./overlays/synthetic-staging`.
  `fleet-system/fleet-milo-configuration` is Ready and renders `./milo`, not
  `./milo-synthetic`. This is the incompatible mixed pairing warned against in
  the stage-1 contract. Host depends on Milo configuration, so a simple change
  to include registration would still install the Fail webhook first.
- Manager and synthetic provider each have one ready replica; provider and
  consumer UI each have two. There is no registration Deployment. Their tag is
  `v0.0.0-feat-manual-inventory-records-20261122-191037`.
  Published Fleet image index digest:
  `sha256:272c4a026c302c62d8511e7da4c08ba89771d204aaf439ae15f044984d079cf4`.
  Provider UI index digest:
  `sha256:01257dc2df0744e119dfc916150d7c771d37bb0dc4399e30e33614b76ab63c3f`.
  Fleet image source labels agree with the merge. Pin immutable digests for the
  eventual reviewed rollout; the Flux sources currently follow a mutable tag.
- Root Milo read confirms `fleet-capacity-registration` already contains four
  `failurePolicy: Fail` entries, all pointing at
  `https://fleet-registration.fleet-system.svc:9443/validate-registration`, with
  **no caBundle**. No request was sent to exercise this broken admission path.
  This is a pre-existing rollout defect, not an instruction to delete the fence
  or change its failure policy.
- The three registration credential/TLS Secrets, registration ledger and host
  registration retention policies are absent. Existing manager credentials are
  not a suitable least-privilege substitute.
- Milo runs `v0.0.0-main-20261120-202331`, whose image source revision is
  `2a15af836be9eab1a4dd55739a92cd661b2f9b03`, not the test dependency version
  `v0.36.9`. Project, Organization and User routing filter files match the pinned
  module byte-for-byte, and the deployed revision's handler chain still wraps
  those decorators inside authentication/impersonation and outside authorization.
  These source comparisons are supporting evidence only;
  deployed routing, impersonation and alternate storage access still need actual
  acceptance. No admission client configuration argument or dedicated
  registration client certificate mount was found on its Deployment.

Baseline reads found 41 Devices and one existing **Enabled** capacity in
`datum-cloud`, plus two organization memberships. Name/UID/resourceVersion-only
snapshots were retained outside Git in a private local evidence directory. The
same snapshots matched on a second read, including resourceVersions. The
existing capacity and synthetic workload must remain unchanged; their presence
rules out treating this provider project as a disposable registration sandbox.
Read-only operator access to the root webhook was denied; inspection instead
used the existing GitOps root credential through a TLS-verified local
port-forward. The temporary credential file was mode 0600 in a 0700 directory
and was removed afterwards. It was not reused as a registration identity.

## Release gate: no live exception granted

The ledger is a 650,000-byte mutable tail plus at most 16 immutable sealed
segments. Each operator-signed `expand` seals the current tail losslessly into
one segment of up to 750,000 bytes and empties the tail; nothing is released or
compacted. A full tail denies growing writes until expansion. Once all 16
segments are sealed, expansion is refused and growth stops until a separately
reviewed storage migration. The [storage runbook](registration-storage-rollout.md)
has the exact budgets and alert thresholds.

That bounds storage, not the number of attempted or failed operations over time.
Even unregistered Device creates/deletes permanently retain incarnations and
aliases, so expansion postpones the stop rather than removing it. A finite
successful smoke test cannot establish safe ongoing operation. Neither PDBs nor
the ledger metrics resolve this problem; they only report how close the stop is.

Bounded lossless expansion is now implemented (enforced tail stop, signed
sealing, `/status` and metrics headroom). It does not implement release, and the
items below still need a reviewed decision against that implementation before
shared staging activation. The decision must specify:

1. Which authority can reconcile each exact operation and lifetime, with current
   authorization, immutable evidence and an audited CAS transition.
2. How pending API-server writes and retries are fenced before deciding an absent
   object cannot still appear. A timeout, a missing object, `Recorded`, or a
   deleted Project is not sufficient proof of release.
3. How aliases, tombstones, unknown outcomes and occupied capacity remain retained
   across crashes, partitions and any migration. Capacity availability must not
   follow ledger compaction or relocation.
4. A byte-budget/headroom model including failed requests, new incarnations and
   alias growth, plus an operational stop/escalation path before exhaustion.
   An external checklist alone is not an enforced growth bound.
5. Concurrency, authorization, restart and uncertain-write tests against actual
   API-server storage. No manual Secret edits, automatic age-based expiry,
   delete/recreate bootstrap, or arbitrary release command is permitted.

If the parent instead seeks bounded acceptance before that implementation, it
must obtain an explicit decision for a **separate disposable Milo authority and
host ledger**, with synthetic identities only, a bounded attempt/byte budget and
no real-inventory routes or shared synthetic service. Preserve that authority's
ledger through the experiment; destroying an isolated test environment is not a
release mechanism for shared staging custody. This is a proposed acceptance
environment, not approval to bypass the shared-staging release gate or claim
that `datum-cloud` is integrated.

## Required infrastructure work (separate approved PR)

The owning repository is `datum-cloud/infra`; no shared checkout was edited.
Use these existing paths as the starting points, not a broad infra merge:

| Path | Required change or evidence |
| --- | --- |
| `apps/fleet-system/staging/reconciliations.yaml` | Separate the registration authority from existing synthetic consumers; explicitly order quiescence, trust/bootstrap, admission and UI activation. Do not simply change the host overlay or relax the live Fail webhook. |
| `apps/fleet-system/staging/source.yaml`, `inventory-source.yaml` | Pin reviewed OCI provenance; preserve independent inventory/source consumers and existing inventory records. |
| `apps/fleet-system/staging/kustomization.yaml` | Include dedicated registration identities, certificates and retention resources through existing declarative mechanisms, only after the release/isolation decision. |
| `apps/fleet-system/staging/manager-config.yaml` | Preserve authority `api.staging.env.datum.net` and discovery paths; agree the complete provider scope rather than narrowing it to hide legacy capacity. |
| `apps/fleet-system/staging/fleet-manager-project-certificate.yaml` | Reference for certificate-to-kubeconfig generation only. Do not copy its `system:masters` grant into registration. Define separate root reads and scoped project get/list/namespace/SAR authority. |
| `apps/datum-control-plane-system/core-control-plane/base/milo-system/apiserver-kustomization.yaml` | Reference for API-server wiring; add a staging-only overlay for dedicated webhook client CA/certificate and admission client configuration, not a shared production change. |

The ordered installation must first prove serving SAN/CA, route reachability and
dedicated client trust without enabling a new Fail webhook on occupied storage.
Startup no longer verifies the fence before listening. The process loads its
credentials, TLS material and operator public key, then listens in a
non-admitting bootstrap that denies every admission. `/transportz` returns 204
on the same mTLS listener without needing the ledger or an installed webhook, so
endpoint trust can be proven first. Admission starts only after a signed
`activate` pins the digest of the installed webhook fence; `/activationz` returns
503 until that activation and the current dependencies verify, and every
admission still runs full fence verification. Do not weaken VerifyFence.

The pod readiness probe is TCP only, so a Ready pod may still be non-admitting.
Gate UI activation on `/activationz` or `fleet_registration_activation_verified`,
not on pod readiness. Prove the sequence in the isolated authority first.
Bootstrap only with create after retention policies and their negative tests;
abort on an existing ledger and validate it rather than replacing it.

Required caller acceptance remains outstanding: real staff proxy identity,
trusted Project extras, named Device read denial, create permission denial,
direct update/status/delete rejection, duplicate contention, TLS client rejection,
unchanged inventory/membership baselines and Unknown recovery. Exercise outage
isolation only on disposable API/storage; never scale down this shared service
or register real assets as smoke tests. UI deployment alone is not acceptance.

## Local safeguard validation

The availability follow-up adds the CAS-exhaustion counter, ledger gauges,
PDB, rolling-update limits and preferred node separation, while keeping all
registration resources out of the synthetic overlay. `/metrics` is served by
the same `Transport()` mux as `/status`, so it has the same dedicated API-server
mTLS boundary and no separate listener. The gauges are listed in the
[storage runbook](registration-storage-rollout.md#enforced-budgets). The PDB and
`maxUnavailable: 0` count TCP-ready pods, which may be non-admitting during
bootstrap or after a fence change; they limit voluntary disruption only. The
obsolete archived review fixture is removed; executable failure/race regressions
remain.

Validated locally with Kubernetes 1.37.0 envtest assets:

- `go test -race -count=1 ./...`, including real Secret contention, metric
  deltas, ledger gauges across bootstrap, activation, pending and completed
  expansion, fence loss and an unreadable root, mTLS rejection of untrusted
  metrics scrapes, and rendered availability and synthetic-isolation assertions.
- `go vet ./...` and `go build ./...`.
- `kustomize build` for `config/milo`, `config/overlays/management-plane` and
  `config/overlays/synthetic-staging`.
- `./hack/validate-overlay-isolation.sh`, `./hack/test-overlay-isolation.sh`,
  `./hack/validate-doc-links.sh` and `git diff --check`.

These checks do not satisfy the outstanding live registration acceptance gates.
