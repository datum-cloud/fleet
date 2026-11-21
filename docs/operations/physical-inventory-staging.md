# Evaluate manual physical inventory in staging

[Manual entry and review](physical-inventory.md#manually-record-a-small-installation) ·
[Optional source comparison](netbox-physical-import.md)

This checklist is for an explicitly approved staging evaluation after independent
adversarial review. It is not approval to deploy or evidence of physical readiness.
No live imports, deployment, source-tag changes, hardware actions, or inventory
writes were performed during the implementation checks below.

## Read-only baseline observed on November 20, 2026

The physical-inventory PR is stacked on `feat/manual-inventory-records` at
`42086bbe2cf05f5d09362fbc777e514907d64462`. Infrastructure configuration was read
at `3bde916629c7b6c49a6820bc40c2546bbf2afdc1`. Preserve that dependent base;
do not retarget the PR or mistake a new artifact publication for deployment.

Read-only staging API queries on that date found:

- Both `fleet-kustomize` and `fleet-inventory-kustomize` configured for
  `v0.0.0-feat-manual-inventory-records`, with artifact digest
  `sha256:45dfd12971b44a92d3d078007cfd41dcb23f4d278d17e3c5ea8f02c65d3f2fdc`.
- `fleet-milo-configuration` reporting Ready with that exact applied artifact.
- `fleet-provider-inventory` reporting Ready at the infrastructure revision above,
  with `prune: false`, `deletionPolicy: Orphan`, and the private
  `apps/fleet-system/staging/provider-inventory` source.
- Manager and provider/consumer UI Deployments referencing the manual-inventory
  build ending `20261120-165214`, with one manager and two replicas of each UI ready.

The three DeviceClaims and one Device in that applied Git source passed offline
creation and unchanged-update validation against the proposed six-kind schemas.
The source files were handled privately outside Git; no real identities were
added to public fixtures. Existing identity and claim-binding immutability also
have negative regression tests. This is schema compatibility, not a live-object
or hardware audit: the management-cluster project API route did not expose the
provider project, so direct live provider-record comparison remains an operator
acceptance step. No production project context was used as a substitute.

## Acceptance gates for an authorized operator

1. Require independent adversarial sign-off and successful CI for the exact PR
   head. In particular, run both formerly optional manager envtest tests with real
   Kubernetes binaries, not skips. Retain the reviewed head and artifact digests.
2. Re-read staging source and applied revisions, the provider project/namespace,
   existing DeviceClaim/Device identities and bindings, and MachineCapacity
   references through the established authorized project route. Keep a private
   before-state. Stop if the baseline changed or the intended target is unclear.
3. Separately approve and review the infrastructure change that selects the exact
   reviewed build. Preserve Flux ownership, provider-project placement, and
   non-pruning inventory reconciliation. Apply the additive CRDs and IAM before
   introducing new record kinds; retain old claims, Devices and capacity unchanged.
4. Manually prepare a small, separately approved cohort using the
   [synthetic six-kind example](../../hack/testdata/physical/manual.yaml) as a shape
   reference. Review premises, references and every cable endpoint. For new
   records use `Installation`, typed components and Cable-only expected topology.
   Include facility-only or rack-membership placement; missing units, electrical
   budgets, verification and readiness must remain Unknown. Do not register an
   asset without a genuine identity or manufacture a serial from a provider ID.
5. Review and merge individual records into the private provider-inventory Git
   source; let Flux be the only API writer. No NetBox access, bulk conversion,
   discovery, sync, or scheduled import is needed. No capacity, billing, IPAM,
   routing, provisioning, hardware, customer-release or status changes are allowed.
6. Verify authorized staff can browse all six kinds, typed components, relationships,
   partial placement and historical bindings. Confirm consumer identities cannot
   read them and inventory roles cannot write status. Test denied and missing
   references without treating them as empty or ready. Verify before/after immutable
   bindings and capacity references privately, including across reconciliation retries.
7. Stop on mismatches, unknown execution or authorization failures. Record these as
   unknown/failed acceptance, not readiness. Even successful acceptance establishes
   only manual recordkeeping and restricted browsing, not qualification or eligibility.

## Rollback without losing inventory

- Record the last known-good manager/UI artifacts and Git revisions before the
  approved rollout. Revert an unsuccessful application change through its existing
  infrastructure GitOps source; do not patch a competing live writer.
- Do **not** blindly downgrade the six-kind schema to the two-kind base after new
  data exists. Retain additive CRDs, typed fields and records until a separate
  compatibility review proves any schema rollback preserves them. Older software
  may not display the new records; missing UI visibility is not permission to delete.
- Keep `prune: false` and `deletionPolicy: Orphan`. Reverting an additive Git entry
  alone does not delete its live object. Never delete/recreate an occupied Device,
  rewrite its identity/claimRef, or release capacity to make rollback appear clean.
- If reconciliation is interrupted, inspect actual records and Flux state, retain
  the before-state and unresolved work, and reconcile only the reviewed Git intent.
  No automatic cleanup, readiness assertion or ownership handover follows a rollback.

## Local validation

Implementation checks passed `go test ./...`, `go test -race ./...` and `go vet
./...` with Kubernetes 1.37.0 envtest assets installed. Both named envtest cases
also passed an explicit uncached race run without skips. The departure regression
and existing active-custody test passed 50 repetitions under the race detector.
The new blocked-handler reproducer failed on the original manager for both direct
and dependency-route reconciliation; that manager code was unchanged from the
stack's base. No safety assertion or timeout was relaxed.

The independent re-engagement reproducer also failed on parent head `449dfcf`
(which includes the patch-panel-position fix) and passed after replacement was
made to drain before publishing. Direct and dependency-route replacements,
cancellation during/after draining, stale pointers, concurrent engagements and
registry ownership changes have deterministic regressions. These and the existing
departure/active-custody tests passed 50 race-detector repetitions. This is local
evidence only: a **new independent adversarial review and fresh CI on the pushed
head remain required** before staging approval.

The manual six-kind fixture, private Git-source compatibility checks, 27 optional
review-aid tests, 58 provider UI tests, 44 consumer UI tests, both UI typechecks and
builds, Milo packaging, all five CI Kustomize renders, overlay isolation, standalone
render/source/preflight and mocked smoke checks, multi-plane harness, shellcheck,
local documentation links and whitespace checks passed. The local Docker daemon
was unavailable, so `hack/test-standalone-build-context.sh` could not complete;
no real disposable standalone smoke was run locally. Require the fresh CI build
context and standalone-smoke gates, not the previous head's results. UI builds
retain non-fatal dependency/Module Federation warnings.

```sh
export KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.25.2 use 1.37.0 -p path)"
go test ./...
go test -race ./...
go vet ./...
go test -race ./internal/manager -run 'TestProject(Reengagement|Departure)|TestConcurrentProjectReengagement|TestDisengageWithActiveCustody' -count=50
go test -race ./internal/manager -run '^(TestManagerEndToEndAgainstSyntheticMilo|TestConsumerDiscoveryIgnoresOtherServicesAndBacksOff)$' -count=1 -v
python3 -m unittest discover -s hack -p test_netbox_inventory.py
pnpm --dir ui/provider typecheck
pnpm --dir ui/provider test
pnpm --dir ui/provider build
go run ./test/acceptance/milo/cmd validate --root .
kustomize build config/milo >/dev/null
bash hack/validate-doc-links.sh
git diff --check
```

There is no live NetBox parity claim. Optional snapshots and candidate conversion
remain solely to help an operator compare a source when separately requested;
they never apply records or change Git. Their synthetic tests are not a source audit.
