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
| Install a real OS over firmware PXE into a disposable local VM | [Local VM PXE installation](local-vm.md) | Single-plane real install, disk-only boot, recovery, cleanup and reuse under explicit TCG; not KVM or hardware qualification |
| Exercise Milo, Karmada and cell-local PXE end to end with one command | [Multi-plane acceptance](multiplane.md) (`task multiplane:acceptance`) | Native project routing, federation delivery/reflection, partition, [Kubernetes site execution](site-execution.md) (Flux-owned shared services, kro-created per-operation Jobs) with worker Pod/Job interruption, delivery and reflection faults, wrong location, cleanup and reuse; every service [installed by per-cluster Flux](gitops.md) from a source built from the checkout; production authority unqualified |
| Drive the same pipeline through Redfish instead of libvirt | [Redfish-mode acceptance](redfish-lab.md) (`task multiplane:redfish-acceptance`) | Redfish executor discovery, one-time PXE/disk boot overrides, power, lost-response recovery and a booted wipe cleanup, against the unmodified DMTF Redfish Interface Emulator with a lab-only libvirt adapter and two persistent lab VMs; synthetic credentials and networking; not physical BMC or hardware qualification |
| Publish physical-side DHCP reservations for network boot | [Boot network](boot-network.md) | Per-asset BMC endpoints and provisioning attachments, a reservation feed that exists only while a boot is admitted, served and withdrawn evidence from the DHCP owner's Kea synchronizer; unit tested, not exercised at a site |
| Import NetBox device records as observed-only evidence | [Observational inventory import](inventory-observation.md) | Allowlisted NetBox facts in `InventoryObservation` status with retained facts on failure and identity guards; no Device, capacity, or hardware effect; unit tested |
| Render deployment bundles and supply environment inputs | [Deployment manifests](../../config/README.md) | Packaging and security boundaries, not runtime readiness |
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
