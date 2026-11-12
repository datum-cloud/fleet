# Fleet operations guide

[Product overview](README.md) · [Functionality](functionality.md) ·
[Architecture](architecture.md) · [Troubleshooting](troubleshooting.md)

Current operational documentation covers development, synthetic validation, and
deployment preparation. It is not a production service runbook: the production
manager remains fail-closed, and real provider integration and live Milo
acceptance are incomplete.

## Choose the right environment

| Task | Guide | What it establishes |
| --- | --- | --- |
| Build and test locally or run the simulator | [Implementation commands](implementation.md#build-test-and-run) | Behavior of the implemented code and synthetic provider |
| Exercise allocation, install recovery, cleanup, and reuse | [Standalone smoke environment](standalone-smoke.md) | Same-cluster lifecycle behavior with namespaces as project stand-ins |
| Render deployment bundles and supply environment inputs | [Deployment manifests](../config/README.md) | Packaging and security boundaries, not runtime readiness |
| Exercise Milo-routed synthetic staging | [Synthetic staging configuration](../config/README.md#environment-owned-inputs) | Restricted synthetic publication and lifecycle behavior, not production authority |
| Validate Milo acceptance packaging and evidence | [Milo acceptance harness](../test/acceptance/milo/README.md) | Fail-closed preflight and sanitized evidence; live acceptance remains blocked |
| Configure protected acceptance workflow access | [GitHub execution boundary](../test/acceptance/milo/github-environment.md) | Required environment and runner-group controls |

Follow each linked guide's prerequisites and commands. In particular, the
standalone smoke scenario requires a fresh dedicated kind cluster and must not
share a cluster with another Fleet controller or retained lifecycle state.

## Deployment ownership

Fleet publishes separate manifest roots for the Milo root control plane and the
Fleet management cluster. Environment-specific configuration, credentials, and
immutable image selection remain in `infra`. See the
[manifest guide](../config/README.md) for exact destinations and required inputs.

Keep standalone and synthetic-staging fixtures separate from production. Do not
promote the synthetic overlay or treat published images, rendered manifests, or
a successful local smoke run as proof that the production service is ready.

## Before enabling infrastructure effects

Review [implementation gaps](implementation.md#explicit-gaps), the
[deployment readiness boundary](../config/README.md#current-readiness-boundary),
and the [roadmap](roadmap.md). Use the
[acceptance requirements](enhancements/machine-lifecycle/acceptance-tests.md) and
[coverage inventory](../test/acceptance/coverage.md) to distinguish required
evidence from checks already implemented.

Never commit credentials, kubeconfigs, provider tokens, or rendered secret-bearing
configuration. Use the acceptance harness's sanitized evidence contract for
results rather than publishing raw logs or object dumps. For blocked runs and
recovery constraints, continue to [troubleshooting](troubleshooting.md).
