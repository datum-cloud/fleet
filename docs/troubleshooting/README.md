# Fleet troubleshooting guide

[Product overview](../README.md) · [Functionality](../functionality/README.md) ·
[Architecture](../architecture/README.md) · [Operations](../operations/README.md)

Identify your environment: standalone smoke test, synthetic staging, or
production. Each supports different operations. Check
[implementation status](../development/README.md) before treating a missing
integration as a bug.

## Common problems

| Symptom | What to check | Detailed reference |
| --- | --- | --- |
| Production manager starts but stays unready | This is intentional while production handlers and authority integrations are missing; manifests alone cannot enable effects | [Current readiness boundary](../../config/README.md#current-readiness-boundary) |
| Manager Pod cannot start or the binary exits during setup | Check the environment-owned ConfigMap, required keys, Milo credential, and named Project; missing or unresolvable inputs fail closed | [Environment-owned inputs](../../config/README.md#environment-owned-inputs) |
| Standalone smoke script refuses a cluster | Check for existing Fleet APIs, fixture namespaces, controllers, and context/cluster mismatch; use the required fresh dedicated cluster | [Smoke prerequisites](../operations/standalone-smoke.md#run-with-kind-and-colima) |
| One standalone claim remains pending | The fixture intentionally offers one machine to two consumers; the second binds only after the first releases and cleanup is verified | [Smoke scenario and expected output](../operations/standalone-smoke.md) |
| Smoke output has blank network and address columns | The fixture requests no network interface and does not exercise NSO/IPAM or routing | [Smoke scope](../operations/standalone-smoke.md) |
| Synthetic staging does not publish or allocate as expected | Check the exact publication contract, distinct projects, allowed profile, and powered-off, networkless claim restriction | [Staging configuration and limits](../../config/README.md) |
| Milo live acceptance returns nonzero or reports `Blocked` | The current harness does not run the live scenario; valid preflight cannot establish `Passed`. Invalid configuration yields `NotRun` evidence when possible | [Acceptance commands](../../test/acceptance/milo/README.md#commands) |
| Acceptance evidence cannot be written or verified | Check the absent-or-empty destination, file permissions, symlink restrictions, and exact three-file evidence contract | [Acceptance evidence](../../test/acceptance/milo/README.md#evidence) |

## Recover safely

If you cannot verify an operation or cleanup result, treat it as unknown, not
successful. Keep the operation and allocation records so recovery can check the
original request. Do not force readiness, remove finalizers, delete ownership
records, or mark occupied capacity as available to unblock a test or rollout.

The [provider execution and recovery contract](../enhancements/machine-lifecycle/README.md#provider-execution-and-recovery)
and [release contract](../enhancements/machine-lifecycle/README.md#release-and-project-lifecycle)
describe the intended guarantees. Compare them with the
[implemented lifecycle library](../development/README.md#standalone-lifecycle-library)
and [simulator limits](../development/README.md#explicit-gaps) before choosing a recovery
action. Real-provider recovery procedures are not established by these fixtures.

## Capture useful evidence

Record the environment mode, source revision or image digest, failing command or
phase, expected outcome, and sanitized observed result. For Milo acceptance, use
the harness's allow-listed evidence files and preserve `Blocked`, `NotRun`, and
`Failed` as distinct outcomes. Do not attach kubeconfigs, tokens, Secret contents,
rendered secret-bearing configuration, or unsanitized logs to reports.
