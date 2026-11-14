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
