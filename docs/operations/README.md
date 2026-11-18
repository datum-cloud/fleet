# Fleet operations guide

[Product overview](../README.md) · [Functionality](../functionality/README.md) ·
[Architecture](../architecture/README.md) · [Troubleshooting](../troubleshooting/README.md)

Use these guides to develop, test, and prepare deployments. They are not
production runbooks. The production manager rejects work that depends on missing
integrations. Real provider support and live Milo acceptance tests are incomplete.

## Choose the right environment

| Task | Guide | What it establishes |
| --- | --- | --- |
| Build and test locally or run the simulator | [Implementation commands](../development/README.md#build-test-and-run) | Behavior of the implemented code and synthetic provider |
| Exercise allocation, install recovery, cleanup, and reuse | [Standalone smoke environment](standalone-smoke.md) | Same-cluster lifecycle behavior with namespaces as project stand-ins |
| Render deployment bundles and supply environment inputs | [Deployment manifests](../../config/README.md) | Packaging and security boundaries, not runtime readiness |
| Deploy requested-hardware resolution | [Request resolver](../../config/README.md#request-resolver) | The production-safe resolver's inputs, role bindings, single-writer rule, and readiness |
| Exercise Milo-routed synthetic staging | [Synthetic staging configuration](../../config/README.md#environment-owned-inputs) | Restricted synthetic publication and lifecycle behavior, not production authority |
| Validate Milo acceptance packaging and evidence | [Milo acceptance harness](../../test/acceptance/milo/README.md) | Fail-closed preflight and sanitized evidence; live acceptance remains blocked |
| Configure protected acceptance workflow access | [GitHub execution boundary](../../test/acceptance/milo/github-environment.md) | Required environment and runner-group controls |

Follow each linked guide's prerequisites and commands. In particular, the
standalone smoke scenario requires a fresh dedicated kind cluster and must not
share a cluster with another Fleet controller or retained lifecycle state.

## Deployment ownership

Fleet publishes separate manifest roots for the Milo root control plane and the
Fleet management cluster. Environment-specific configuration, credentials, and
immutable image selection remain in `infra`. See the
[manifest guide](../../config/README.md) for exact destinations and required inputs.

Keep standalone and synthetic-staging fixtures separate from production. Do not
promote the synthetic overlay or treat published images, rendered manifests, or
a successful local smoke run as proof that the production service is ready.

## Requested-hardware resolution

`fleet-request-resolver` is the only Fleet workload intended to run in
production today. It pins `status.requestedHardware` and `RequestResolved` on
Machines in every project that consumes the `metal` service, and has no
lifecycle, provider, quota, or private-record effect. Fleet ships the binary,
image, Milo roles, and the opt-in `config/components/request-resolver`
component. `infra` must still supply its configuration, kubeconfigs, role
bindings, and image pin; until then production Machines stay unresolved. See
the [deployment inputs](../../config/README.md#what-the-environment-must-supply).

Run one writer per project. Do not let the resolver and an enabled
`fleet-manager` projector serve the same project; disable the manager's with
`--enable-requested-hardware-projector=false`. Keep `fleet-manager` at zero
replicas in production. The resolver does not need it.

To diagnose an unready resolver, compare the projects with an Active `metal`
ServiceConsumer against the `consumer_provider_engaged_clusters` metric and the
`/readyz` message, which names the first project that is not engaged or not
synced. A Machine with `RequestResolved=False` reports its profile problem
(`ProfileNotFound`, `ProfileDeleting`, or `ProfileInvalid`); a Machine with no
condition at all is in a project the resolver has not engaged.

## Before enabling provider changes

Review [implementation gaps](../development/README.md#explicit-gaps), the
[deployment readiness boundary](../../config/README.md#current-readiness-boundary),
and the [roadmap](../development/roadmap.md). Use the
[acceptance requirements](../enhancements/machine-lifecycle/acceptance-tests.md) and
[coverage inventory](../../test/acceptance/coverage.md) to distinguish required
evidence from checks already implemented.

Never commit credentials, kubeconfigs, provider tokens, or generated configuration
that contains secrets. Share the acceptance tool's sanitized results, not raw
logs or object dumps. For blocked runs, see
[troubleshooting](../troubleshooting/README.md).
