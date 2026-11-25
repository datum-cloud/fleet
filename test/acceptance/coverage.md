# Acceptance implementation coverage

The detailed inventory in
[`docs/enhancements/machine-lifecycle/acceptance-tests.md`](../../docs/enhancements/machine-lifecycle/acceptance-tests.md)
is authoritative. This table distinguishes executable safeguards from live
integration evidence.

| Area | Executable evidence | Current state |
| --- | --- | --- |
| Exact provider and two-consumer Project lifetimes | Config validation requires distinct names and UIDs | Offline verified only |
| Exact Fleet and Milo revisions and images | Config validation requires commit revisions and image digests | Offline verified only |
| Missing authority cannot become success | `TestRunBlocksBeforeLiveScenario` | Verified fail-closed |
| Invalid or ambient credentials cannot start | Config requires explicit absolute credential paths | Verified fail-closed; files are not loaded |
| Sanitized evidence | `TestEvidenceExcludesCredentialPathsAndUsesRestrictedModes`, `TestResultRejectsFreeFormEvidencePayloads`, `TestVerifyEvidenceContentRejectsNonCanonicalArtifacts`, and `TestVerifyEvidenceUsesOneHeldDescriptorSnapshot` | Closed reason/blocker codes and one bounded held-descriptor snapshot verified |
| Bounded config input | `TestLoadConfigRejectsOversizedFileBeforeDecode`, `TestLoadConfigRejectsUnsafePathsAndModes`, and `TestLoadConfigAncestorSwapCannotRedirectHeldWalk` | Owner-only component-walked descriptor read is bounded before decode |
| False-success prevention | `TestResultPassedRequiresExactCompletePhases` and `TestResultRejectsContradictoryPhaseGrammar` | Exact terminal phase grammar verified |
| Status/error consistency | `TestValidateOutcomeRejectsEveryStatusErrorMismatch` | All result/error combinations verified |
| Workflow log sanitization | `TestLiveDoesNotPrintRawExecutionError` | Fixed reviewed failure code only |
| Fresh exact artifacts | `TestEvidenceDirectoryMustBeFreshAndExact` | Verified |
| Credential path aliases and modes | `TestCredentialFilesRejectLexicalAndFilesystemAliases` | Already-open no-follow descriptors, re-stat, owner-only mode, and parent/final symlink rejection verified |
| Manual workflow boundary | `TestValidateRepositoryProtectsWorkflowBoundary` | Reviewed pins, run-unique absent output, live-step-only config, and exact upload allowlist verified |
| Two consumers competing for one machine | No live executor | Blocked |
| IAM, entitlement, quota, revocation ordering | Owning-service protocols unavailable | Blocked |
| NSO/IPAM allocation and Galactic programming | No production authorization or location integration | Blocked |
| Install, reinstall, lost response, restart, and stale worker | Unit contracts exist elsewhere; no live executor | Blocked |
| Cleanup, quota settlement, reuse, and project purge | Purge and protected-custody protocols unavailable | Blocked |

Offline validation is not Milo acceptance. No row becomes accepted until the
required owning service participates in a protected live run and the sanitized
result reports every required phase `Passed`.

## Device custody and operations

These are the acceptance tests of [#174 at `23c583f`](https://github.com/datum-cloud/fleet/pull/174).
Increment A covers them against a real API server. End-to-end coverage arrives
in Increments C and D. See [device custody](../../docs/development/device-custody.md#tests).

| #174 acceptance test | Executable evidence | Current state |
| --- | --- | --- |
| Two allocators race for one server; one wins | `TestConcurrency` | Envtest verified; end to end proposed |
| An allocator retries after a lost response and claims no second server | `TestConcurrency` | Envtest verified for the custody claim; naming from the Machine UID arrives in C |
| A staff operation names a customer allocation and is refused | `TestAdmissionPreconditions`, with the test-only staff kind | Envtest verified; end to end deferred to power |
| A commissioning times out and a later one settles the hold | `TestLifecycle`, `TestRecovery`, with the test-only kind and `DeviceWipe` | Deferred: commissioning is out of scope; the mechanism is envtest verified |
| A staff operation targets an allocated, releasing or unsettleable held server and is refused, nothing sent | `TestAdmissionPreconditions`, `TestRecovery` | Envtest verified; end to end proposed |
| A worker passes its deadline; `OutcomeUnknown`; settling waits for the worker to stop | `TestLifecycle`, `TestRecovery` | Envtest verified with a fake site; end to end proposed |
| A delayed worker's step after a newer admission is refused by the journal | None yet | Proposed in B |
| Custody deleted and restored stays unallocatable until rebuilt with a higher fence | `TestRebuild` | Envtest verified with a fake site; end to end proposed |
| A Machine deleted during installation; release waits; a wipe settles the install hold | `TestLifecycle`, `TestRecovery` | Envtest verified; end to end proposed |
| No staff role can read a bootstrap payload | `TestRolesKeepOneWriterAndKeepStaffFromCustomerSecrets` | Role definitions verified; bootstrap stays refused |

The emulator lab cannot cover BMC TLS key rotation, vendor Redfish behavior,
UEFI or real PXE firmware, real disks and wipe assurance, real power reset
evidence, clock skew between coordinator and site, more than one site serving
a Device, or a permanently lost site. None of these tests qualifies hardware.
