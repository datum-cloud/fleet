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

These are the acceptance tests of [#174 at `d4d17f9`](https://github.com/datum-cloud/fleet/pull/174).
Increment A covers them against a real API server, and Increment B covers the
site side in unit tests. Increment C runs allocation, installation, release and
reuse end to end. Increment D adds the failure scenarios to both multi-plane
runs, the local VM and the Redfish lab, through real Milo project APIs, Karmada,
kro and the site. See [device custody](../../docs/development/device-custody.md#tests)
and the [multi-plane phases](../../docs/operations/multiplane.md#acceptance-phases).

| #174 acceptance test | Executable evidence | Current state |
| --- | --- | --- |
| Two allocators race for one server; one wins | `TestConcurrency`, `TestConcurrentAllocatorsOneWinner`; the `verify-contention` phase | Envtest verified for true concurrency. End to end, two consumers contend and custody names the one MachineAllocation |
| An allocator retries after a lost response and claims no second server | `TestConcurrency`, `TestAnAllocationThatLostItsDeviceIsReplaced` | Envtest verified; the allocation is named from the Machine UID, so a retry finds the same one |
| A staff operation names a customer allocation and is refused | `TestAdmissionPreconditions`, with the test-only staff kind | Envtest verified; end to end deferred to power, the first staff kind with an effect |
| A commissioning times out and a later one settles the hold | `TestLifecycle`, `TestRecovery`, with the test-only kind and `DeviceWipe` | Deferred: commissioning is out of scope. The same hold and settle mechanism runs end to end for an installation and a wipe |
| A staff operation targets an allocated, releasing or unsettleable held server and is refused, nothing sent | `TestAdmissionPreconditions`, `TestRecovery`; the `deadline-hold` phase | Envtest verified. End to end, a `DeviceRecovery` naming the held installation at the wrong fence is refused for `HoldMismatch` on a releasing Device; custody, the site store and the BMC audit are unchanged |
| A worker passes its deadline; `OutcomeUnknown`; settling waits for the worker to stop | `TestLifecycle`, `TestRecovery`, `TestAWorkerStoppedAtItsDeadlineClosesItsAdmission`; the `deadline-hold` and `deadline-settled` phases | End to end in both runs: the installation ends `OutcomeUnknown` with `DeadlineExceeded`, and the settling wipe is admitted only after its worker stopped |
| A delayed worker's step after a newer admission is refused by the journal | `TestTwoWorkersOnOneDevice`, `TestANewerFenceStopsTheOlderWorker`, `TestStoreRefusesOlderFencesAndSurvivesARestart`; the `delivery-faults` phase | End to end: an assignment below the stored fence fails its Job before any effect, with the journal and the BMC audit unchanged |
| A TLS pin mismatch or an identity mismatch sends nothing | `TestPinMismatchSendsNothing`, `TestIdentityMismatchSendsNothing`; the `redfish-discovery-faults` phase | End to end in the Redfish run: a key other than the pin reaches no request, and a swapped or unknown identity sends only reads |
| Custody deleted and restored stays unallocatable until rebuilt with a higher fence | `TestRebuild`; the `rebuild` phase | End to end: rebuilt `Unverified` while the site cannot report, a new Machine finds no capacity, then `Verified` above the site's fence |
| A Machine deleted during installation; release waits; a wipe settles the install hold | `TestLifecycle`, `TestRecovery`; the `machine-delete`, `deadline-hold` and `deadline-settled` phases | End to end: release holds the Device with the installation active and no wipe, then the wipe settles the hold and the Machine is gone |
| No staff role can read a bootstrap payload | `TestRolesKeepOneWriterAndKeepStaffFromCustomerSecrets`; the `staff-access` phase | Role definitions verified. End to end, staff are forbidden Secrets and every write but recoveries. Bootstrap stays refused, so no payload exists to read |
| A tenant cannot trigger an operation on hardware it does not hold (a review request beside #174's list) | `TestAdmissionPreconditions`; the `tenant-isolation` phase | End to end: while consumer A holds the Device, consumer B's project serves no Device API, the allocator refuses to install for B, and an installation the Metal provider names for B's unheld allocation is refused for `HolderMismatch`, with the fence, the journal and the BMC audit unchanged and nothing delivered |
| Importing a Device sends nothing and makes no capacity Available | `TestImportedDeviceGetsUnverifiedCustodyAndSendsNothing`; the `import-inert` phase | End to end: no operation, assignment, Job, journal entry or BMC effect. The capacity projector is not wired, so the run's fixture writes capacity status itself |

The emulator lab cannot cover these:

- BMC TLS key rotation, or one key per server: the lab serves both targets from one BMC with one key, so only target A is a Device;
- vendor Redfish behavior, UEFI or real PXE firmware;
- real disks, wipe assurance or power reset evidence;
- clock skew between the coordinator and a site;
- more than one site serving a Device, or a site lost for good;
- true parallel races between processes, which envtest covers.

The multi-plane Milo registers no creator webhook, so the multi-plane runs do not prove it. None of these tests qualifies hardware.
