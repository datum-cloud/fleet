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
| Exercise allocation, install recovery, release with a wipe, and reuse | [Standalone smoke environment](standalone-smoke.md) | Same-cluster lifecycle on Device custody, with a simulator site and namespaces as project stand-ins |
| Understand the local VM site that the multi-plane run drives | [Local VM site](local-vm.md) | The workers that install a real OS over firmware PXE into a disposable libvirt domain, boot it from disk, and wipe it; the single-plane harness is retired; not KVM or hardware qualification |
| Exercise Milo, Karmada and cell-local PXE end to end with one command | [Multi-plane acceptance](multiplane.md) (`task multiplane:acceptance`) | Native project routing, the custody coordinator, federation delivery/reflection, partition, [Kubernetes site execution](site-execution.md) (Flux-owned shared services, kro-created per-operation Jobs) with worker Pod/Job interruption, delivery and reflection faults, wrong location, release with a wipe and reuse; verification of the run on the custody path is pending; every service [installed by per-cluster Flux](gitops.md) from a source built from the checkout; production authority unqualified |
| Drive the same pipeline through Redfish instead of libvirt | [Redfish-mode acceptance](redfish-lab.md) (`task multiplane:redfish-acceptance`) | Redfish site workers: per-Device credentials over a pinned TLS key, one-time PXE/disk boot overrides, power, lost-response recovery and a booted wipe, against the unmodified DMTF Redfish Interface Emulator with a lab-only libvirt adapter and two persistent lab VMs; synthetic credentials and networking; verification of the run is pending; not physical BMC or hardware qualification |
| Publish DHCP reservations for network boot | [Boot network](boot-network.md) | Boot addresses claimed from IPAM per installation and wipe, boot NICs from Device inventory, a reservation feed read from the site store that exists only while a boot is admitted, served and withdrawn evidence from the Kea synchronizer; tested against a real API server and unit tested, not exercised at a site |
| Plan a rack slot and register the server bound to it | [Planning and registering physical devices](physical-inventory.md) | Operator-written `DeviceClaim` and `Device` records in the provider project in Milo, with immutable identity and binding, shown read-only in the staff portal; no capacity or hardware effect; unit tested |
| Record what each Device's BMC reports | [BMC discovery](bmc-discovery.md) | Read-only Redfish discovery into Device status, with TLS public-key pinning approved per BMC and identity compared with spec; unit and TLS end-to-end tested, not qualified on vendor hardware |
| Render deployment bundles and supply environment inputs | [Deployment manifests](../../config/README.md) | Packaging and security boundaries, not runtime readiness |
| Run the manager across consuming projects | [Project discovery](../../config/README.md#project-discovery) | Discovery, per-project isolation, departure, role bindings, and readiness |
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

## Running the manager across projects

`fleet-manager` serves every project that consumes the `metal` service; see
[project discovery](../../config/README.md#project-discovery). In production it
has no provider and no synthetic integration, so its lifecycle handlers stay
blocked: no allocation, installation, provider call, or custody write. The
production Milo root registers the Device custody kinds and their roles, but
nothing runs the custody coordinator there. The only
effect is pinning `status.requestedHardware` and `RequestResolved` on Machines.
`/readyz` reports discovery and cache health, not provider readiness, and
passing it enables nothing. Fleet ships the code, Milo roles, and manifests;
`infra` supplies the configuration, kubeconfigs, role bindings, and image pin.
See the [environment inputs](../../config/README.md#environment-owned-inputs).

To diagnose an unready manager, compare the projects with an Active `metal`
ServiceConsumer against the `consumer_provider_engaged_clusters` metric and the
`/readyz` message, which names the first project that is not engaged or not
synced. A project that does not serve every API the manager watches never
engages. A Machine with `RequestResolved=False` reports its profile problem
(`ProfileNotFound`, `ProfileDeleting`, or `ProfileInvalid`); a Machine with no
condition at all is in a project the manager has not engaged.

A project that leaves the consumer set is no longer reconciled, even with
active custody; nothing is released or rewritten. Re-engaging it resumes.
Production releases for a departed project require it to be engaged again.

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
