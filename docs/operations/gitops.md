# Flux-reconciled local environment

The [multi-plane acceptance](multiplane.md) installs every long-running service
the way a real environment would: each cluster runs its own Flux, and Flux
reconciles Helm releases and Kustomizations from one OCI source artifact.
That artifact is built from the current checkout, including uncommitted
changes. The harness no longer runs `helm install`, `kind load`, or
`kubectl apply` for any service, and no Milo API runs as a bare Docker container.

This is an **implemented local composition**. It shows that the manifests,
dependency ordering, ownership and retention policies work under real Flux
controllers. It does not qualify production deployment: the seams are listed
under [what is not production](#what-is-not-production).

## Ownership

```text
                    run registry  (registry:3.0.0, plain HTTP on the kind network)
                    ├── fleet-gitops:local   OCI artifact: config/multiplane + config/site
                    │                        + generated environment/<cluster>
                    └── fleet-site@sha256:…  site image
        ┌───────────────────┬──────────────┴─────────────┐
   platform cluster     hub cluster (host)           cell cluster
   Flux v2.9.6          Flux v2.9.6                  Flux v2.9.6
   ├ milo-provider      ├ karmada (HelmRelease)      ├ fleet-coordination  (CRD, ns)
   ├ milo-consumer-a    └ karmada-system/…           ├ fleet-federation    (member identity)
   └ milo-consumer-b                                 ├ kro (OCI chart HelmRelease)
                          ├ fleet-karmada-coordination├ fleet-site-base     (ns, Retain PVC, identities,
                          ├ fleet-karmada-api         │                      RBAC, kro grants, admission)
                          └ fleet-karmada-members     ├ fleet-site-services (runtime, boot, observer,
                            (applied to the Karmada API│                      boot Service; scoped identity)
                             through karmada-kubeconfig)├ fleet-site-graph  (RGD; scoped identity)
                                                      └ fleet-site-dispatch (HandoffDispatcher; scoped)
                                                              │
                                                              ▼
                                                     kro: per-operation ServiceAccount, Role,
                                                     RoleBinding and worker Job only
```

In synchronized lab DHCP mode (`run.sh --redfish --synced-dhcp`), the platform
cluster also reconciles `ipam-system` (IPAM's namespace, its PostgreSQL and a
NetworkPolicy toward the provider Milo API, from `config/multiplane/ipam`) and
`ipam-apiserver` (IPAM's published `ipam-kustomize` bundle, pinned by digest,
from its own OCIRepository). See
[IPAM in the lab](redfish-lab.md#ipam-in-the-lab).

| Surface | Writer | Never written by |
| --- | --- | --- |
| Flux controllers and sync objects (`flux-system`) | Bootstrap once, then each cluster's own Flux (`fleet-cluster` reconciles `environment/<cluster>/flux-system`) | — |
| Milo project API Deployments, etcd StatefulSets, PVCs, Services | Platform Flux (`milo-<role>`) | Harness (it suspends the Kustomization before fault injection) |
| Karmada chart | hub helm-controller (`HelmRelease karmada`) | Harness (suspended during reflection faults) |
| Handoff CRD, management role, interpreter, member `Cluster`, `PropagationPolicy` on the Karmada API | hub kustomize-controller through the chart's `karmada-kubeconfig` | — |
| kro controller | cell helm-controller (`HelmRelease kro`) | — |
| Site namespace, StorageClass, site store PVC, service identities, observer RBAC, kro grants and admission policies | cell Flux (`fleet-site-base`) | kro does not manage these; its delete grants still cover service identities and coordination RBAC (see trust model) |
| Shared runtime, boot and observer Deployments, boot Service, worker-policy parameter, delivery declaration | cell Flux as `fleet-site-services-reconciler` | kro (read only; verified by label, inventory and RBAC), harness (drift is injected, repair is Flux's) |
| `ResourceGraphDefinition fleet-handoff-dispatcher` | cell Flux as `fleet-site-graph-reconciler` | — |
| `HandoffDispatcher/dispatcher`, including `dispatch` | cell Flux as `fleet-site-dispatch-reconciler`, from a source revision | Harness (dispatch is a pushed revision; the graph-lifecycle fault suspends the layer first) |
| Per-operation `w-<handoff>` ServiceAccounts, Roles, RoleBindings and Jobs | kro only, admission-checked | Flux (verified absent from every inventory) |
| `MachineAllocationHandoff` objects and their status | Management/Karmada (intent), site observer (status) | Flux, kro |
| Device custody, operation and allocation records in the provider project | The custody coordinator, the Metal provider and the release controller, each with its own token | Flux, and every source revision |
| Credentials: Milo static tokens and serving keys, ServiceAccount tokens, Karmada member Secret | Bootstrap credential enrollment | Any source revision |

Fleet and Karmada stay the operation authority and delivery path. Nothing per
allocation is written to the source: handoffs, Jobs and results never appear
in an artifact or a Flux inventory.

## Bootstrap boundary

`hack/multiplane/run.sh` performs only these imperative steps, each a bounded
phase:

1. **`topology`** (`topology.sh`) creates the run registry and three kind
   clusters with memory caps, the cell's registry mirror configuration and
   the reversible inotify adjustment. It installs no service.
2. **`site-image-push`** pushes the built site image; the cell pulls it by digest.
3. **`flux-tools`** downloads the Flux v2.9.6 CLI and `install.yaml` and
   verifies both against SHA-256 values pinned in `hack/multiplane/flux.py`.
4. **`flux-source`** stages revision 1 and pushes it with
   `flux push artifact --reproducible`.
5. **`flux-bootstrap-<cluster>`** applies that revision's Flux components,
   waits for the CRDs, then applies the two sync objects (`OCIRepository
   fleet-gitops`, `Kustomization fleet-cluster`). This is what
   `flux bootstrap` commits as `gotk-components.yaml` and `gotk-sync.yaml`. The
   apply uses kustomize-controller's field manager, so Flux takes these
   objects over without a second owner. From then on each cluster's Flux
   reconciles its own controllers from the source.
6. **Credential enrollment.** `milo.py` writes each Milo API's
   `milo-apiserver-private` Secret into the namespace Flux created. `roles.py`
   mints 24-hour tokens for the Flux-created management, custodian and
   federation ServiceAccounts, and writes the Karmada member Secret.
7. **Tenant fixture bootstrap.** `milo.py` creates the Milo Projects,
   static-token RBAC grants and metal CRDs inside each project API, and the
   management CLI seeds catalog fixtures. These are data inside running
   services, not service installation, and are labelled synthetic fixtures.

Everything else is a Flux reconciliation of a pushed revision.

## Source revisions

`flux.py stage` copies `config/multiplane` and `config/site` from the
checkout into `<state>/gitops/r<N>`. It stages the verified `install.yaml`
with `config/multiplane/flux/components.yaml`, unchanged, as
`environment/flux-components`, and adds a generated `environment/<cluster>`
per cluster. The environment holds the
sync objects and a non-secret `fleet-environment` ConfigMap: cell name,
member endpoint, scoped asset, digest-pinned site image, dispatch flag and a
revision marker. Before pushing, it refuses any file that contains a `Secret`
manifest, a private key, a token-shaped field, or any credential retained in
the run state. It also requires every environment to render with `kustomize`.
The per-file SHA-256 list is copied to `evidence/gitops-r<N>.SHA256SUMS`.
`flux-revisions.json` records the digest, Git HEAD and dirty-checkout content
hash.

| Revision | Purpose |
| --- | --- |
| r1 | Platform services: Milo, Karmada, host RBAC, Karmada API configuration, kro, site base and graph |
| r2 | Adds the shared site services and the `HandoffDispatcher` instance with `dispatch: false` once the scoped asset is known |
| r3 | `dispatch: true`, pushed while the hub is paused; the cell's Flux applies it from the registry |
| r4 | Fail-closed: kro chart pinned to a digest no registry serves |
| r5 | Recovery: pinned kro chart restored |
| r6 | Retention: the site store PVC removed from the source |
| r7 | Retention: the PVC restored to the source |

## Versions and pins

| Component | Pin | Why |
| --- | --- | --- |
| Flux | v2.9.6: `install.yaml` SHA-256 `9c1fda7e…4251c`; CLI `flux_2.9.6_darwin_arm64.tar.gz` SHA-256 `7008a758…c126`; controllers by digest in `config/multiplane/flux/components.yaml` (source v1.9.6, kustomize v1.9.6, helm v1.6.5, notification v1.9.4) | v2.10.0 (2026-10-26) requires Kubernetes ≥ 1.35.2, and the exercised kind node is v1.35.0. v2.9.6 (2026-10-01) is the newest patch of the newest compatible minor, and datum-cloud/infra staging runs v2.9.5. Image automation and source-watcher are removed; notification-controller is kept so controller events have a receiver. |
| Source | OCI artifact `fleet-gitops:local` in the run registry, `registry:3.0.0@sha256:6c5666b8…457e` | The Flux OCI pattern Datum uses for `*-kustomize` bundles, with no external publication of uncommitted changes |
| Karmada | chart v1.16.0 from `HelmRepository` `raw.githubusercontent.com/karmada-io/karmada/master/charts`; chart archive SHA-256 `773ab194…34fe` verified in Flux's HelmChart artifact | Same chart as the connected test-infra federation. The repository and release shape follow datum-cloud/infra `infrastructure/karmada`. The chart's floating `cfssl` and `kubectl` hook images are now pinned by index digest. |
| kro | `OCIRepository` `oci://registry.k8s.io/kro/charts/kro` at manifest digest `sha256:4e8de4d3…a67c` (chart layer `ceeb6307…`), image `v0.9.4@sha256:eaf9fbad…` | Unchanged from [site execution](site-execution.md#versions); `OCIRepository` as a chart source is the Flux-native way to pin an OCI chart by digest |
| Milo | `ghcr.io/milo-os/milo@sha256:3b4f2890…` (v0.32.5 multi-arch index: arm64 on Colima, amd64 in CI mode); etcd `registry.k8s.io/etcd:3.5.16-0@sha256:c6a9d11c…` | Same Milo build as before, now as Flux-managed workloads |

## Dependency, health and upgrade policy

- **Dependencies.** Child Kustomizations use `dependsOn`, so a dependent is
  applied only when its dependencies are Ready at their current generation.
  The order is: Karmada → API coordination → Karmada API → members; and on
  the cell: coordination → federation; coordination → site base → shared
  services; kro and site base → graph; graph, services and federation →
  dispatcher. Shared site infrastructure does not depend on kro. The only
  controller tuning is `--requeue-dependency=10s` (default 30s) on
  kustomize-controller and helm-controller, so a revision ripples through the
  site chain faster. Flux manages that setting on its own
  controllers like any other source change. The Helm
  releases are wrapped in Kustomizations with `wait: true`, so a release and
  its chart source must both be healthy before anything depends on them.
- **Health.** `wait: true` everywhere, with CEL `healthCheckExprs` for kinds
  kstatus cannot judge:
  - RGD `status.state == 'Active'`;
  - HandoffDispatcher `status.state == 'ACTIVE'`, which kro reports only when
    the Flux-owned runtime, boot and observer Deployments and boot Service it
    references are ready;
  - Karmada `Cluster` Ready;
  - PVC `Pending` or `Bound`, because the class is `WaitForFirstConsumer`.

  The Milo Deployments, etcd StatefulSets, kro and the shared site Deployments
  use kstatus.
- **CRDs.** The handoff CRD Kustomizations use `prune: false` and the CRD is
  annotated `prune: disabled`, as datum-cloud/infra does for Karmada CRDs.
  kro installs its CRDs on install and skips them on upgrade.
- **Helm upgrades.** kro: `driftDetection: enabled`, remediation retries with
  rollback. Karmada: `driftDetection: warn`, as in datum-cloud/infra, because
  its pre-upgrade hooks regenerate certificates. One upgrade retry, then
  rollback. Both use `upgrade.crds: Skip`.
- **Scoped identities.** Three site layers impersonate ServiceAccounts with
  narrow grants:
  - `fleet-site-services`: Deployments, Services and ConfigMaps in `fleet-site`,
    plus read access for health checks;
  - `fleet-site-graph`: `ResourceGraphDefinitions` only;
  - `fleet-site-dispatch`: `HandoffDispatchers` in `fleet-site` only.

  None of these identities can directly write Jobs, volumes, RBAC, admission policy or
  handoffs. Source authors remain trusted. The services identity can create a
  Deployment using the observer ServiceAccount and thereby obtain its status-write
  authority, or create a privileged workload. The services layer chooses the
  privileged runtime image. Graph authors choose the worker template within the
  admission policy. The same source controls the admin-applied site base. This
  is not a sandbox for untrusted site revisions; see the
  [trust model](site-execution.md#ownership-and-trust-model). The other
  Kustomizations use the controller identity, as in a standard single-tenant
  bootstrap.

## Retention and pruning

Root and child Kustomizations prune. These objects are annotated
`kustomize.toolkit.fluxcd.io/prune: disabled`, so removing them from the
source, or deleting their Kustomization, never deletes them:

- the site and coordination namespaces;
- the handoff CRD;
- `fleet-site-retain` and the site store PVC;
- the RGD and the `HandoffDispatcher`;
- the Karmada `HelmRelease` and member `Cluster`;
- each Milo namespace.

The shared Deployments and boot Service are prunable by their own layer only:
removing one from the source deletes it, which stops new work and, for the runtime,
any running guest. That leaves operations Unknown rather than released. No graph
or instance lifecycle can delete them.

The StorageClass and PVC are also `ssa: IfNotPresent`: Flux creates them once
and never updates them. A reconcile, a controller restart or a source change
therefore cannot replace the site store volume. A fresh store would report a lower
fence than the one last admitted; see the
[known limitations](multiplane.md#known-limitations-of-the-local-mvp).
Retiring a site or unjoining a member is an ordered manual procedure, as in
datum-cloud/infra's Karmada runbook.

Pruning is still not cancellation. A source revision that changes `dispatch`
has the same effect as the
[documented kro pruning behavior](site-execution.md#lifecycle-and-retention).

## Acceptance coverage

These phases run in addition to the existing scenarios; see
[acceptance phases](multiplane.md#acceptance-phases).

| Phase | Asserts | Evidence |
| --- | --- | --- |
| `flux-verify` | Each cluster's source is at the pushed digest. Every expected Kustomization and HelmRelease is Ready at the current generation and revision. The kro chart source is at the pinned digest, the Karmada chart artifact matches the pinned SHA-256, and controllers are digest-pinned. Ownership audit: the services, graph and dispatch inventories are exactly their objects. Each shared Deployment, Service, ConfigMap, ServiceAccount, Role, binding, policy and the PVC carries its one Flux layer label, is in that inventory, and has no kro label or owner reference. kro has written nothing before dispatch. No Job, Pod, handoff, Secret or `w-` object is in any Flux inventory. Shared layers do not depend on kro. Protected objects carry `prune: disabled`. | `flux-verify.json`, `flux-initial.json` |
| `rbac` | The revoked observer RoleBinding is restored by kustomize-controller under a new UID, not by the harness. As kro's identity, 8 worker-scoped verbs are allowed and 15 others denied, including Deployment, Service, PVC and ConfigMap writes, Jobs outside `fleet-site`, Secrets and handoff status. Server-side dry runs admit worker-shaped objects. They deny, by policy, a Job running as the observer, a privileged Job, another image, a hostPath volume and the host network; a broader Role; a RoleBinding to another Role; and a ServiceAccount outside the worker set. | `flux-rbac-restore.json`, `kro-rbac.json` |
| `flux-graph-lifecycle` | With the install terminal and its workers present, the ownership audit passes. The dispatcher's layer is suspended and the instance deleted. kro removes exactly that handoff's Job, ServiceAccount, Role and RoleBinding. Every shared object, the RGD and the StorageClass keep their UIDs, and the PVC/PV, site store and custody are unchanged. Resuming lets Flux recreate the instance under a new UID, and the recreated Job replays a verified no-op (exit 0). Orphaned terminal Pods are listed. | `flux-graph-lifecycle.json` |
| `partition`, `partition-readiness` | Consumer A is stopped by suspending its Kustomization and scaling it to zero. The suspension, UID, generation, zero replicas and no Pods survive the platform root reconciling r3. Resuming leaves Flux to restore it. | `flux-partition.json`, `flux-reconciles.json` |
| `dispatch` | Dispatch arrives as r3 through the cell's Flux while the hub is paused | `flux-revisions.json` |
| `heal` | Flux on every cluster acknowledges fresh requests and is Ready at the current revision after both node restarts | `flux-reconciles.json` |
| `flux-drift` | Five out-of-band changes are each corrected by Flux, with the site store, Jobs, PVC/PV and custody unchanged. helm-controller restores the kro Deployment scaled to 0 (same UID). kustomize-controller recreates the Karmada interpreter deleted on the Karmada API, and reverts a widened `fleet-site-kro` grant. As `fleet-site-services-reconciler`, it recreates the deleted shared boot Service under a new UID with the same fixed ClusterIP, ports and selector, and no kro label. It also restores the shared observer Deployment scaled to 0 (same UID). The dispatcher returns to `ACTIVE` and the ownership audit passes. | `flux-drift.json` |
| `flux-restart` | Every Flux controller Pod on the three clusters is deleted. Each source, Kustomization and HelmRelease then acknowledges a request token issued after the restart. No Helm release revision, inventory, Job, site store or custody changes. | `flux-restart.json`, `flux-reconciles.json` |
| `flux-fail-closed` | r4 leaves the kro chart source and Kustomization not Ready. The graph and dispatcher report `DependencyNotReady` and their new revision is not applied (marker unchanged). The site base and shared services layers are Ready at r4, with every shared object UID and Deployment generation unchanged. The running kro Deployment and site are untouched. r5 converges. | `flux-fail-closed.json` |
| `flux-retention` | r6 drops the PVC from the inventory without deleting it (same PVC/PV UID, site store and custody). r7 re-adopts the same claim. | `flux-retention.json` |
| `flux-final` | All three clusters are at the final revision with nothing suspended, and the ownership audit passes with the cleanup workers present | `flux-final.json`, `flux-ownership-final.json` |

## What is not production

- **Source transport.** A plain-HTTP, unauthenticated, unsigned registry on the
  disposable kind network (`OCIRepository.spec.insecure`). Production needs TLS,
  registry credentials and `spec.verify` (cosign or notation).
- **Credentials.** Credentials are enrolled imperatively. They are never in
  the source and are scanned for, but there is no SOPS, External Secrets or
  workload identity, and tokens are not rotated.
- **Cluster and Milo topology.** Three kind clusters on one VM, not
  independent failure domains. Milo project APIs are Flux-managed workloads on
  one platform cluster, with static-token RBAC, self-signed serving keys and
  fixture Projects. This is not Milo's project provisioning, IAM or HA
  storage.
- **Flux lockdown.** The Karmada API is reconciled with the chart's admin
  kubeconfig. datum-cloud/infra uses a dedicated, secretless Flux identity
  there. Multi-tenancy lockdown flags, such as `--no-cross-namespace-refs` and a
  default ServiceAccount, are not set; only the site layers impersonate scoped
  ServiceAccounts.
- **Upstream sources.** Karmada's HelmRepository and the kro chart come from
  upstream over the internet. No mirror or signature check is configured.
  Pins are verified in evidence (Karmada) or enforced by digest (kro).
- **Acceptance driver.** The management phase CLI (`fleet-multiplane-host`)
  remains an explicit acceptance driver run on the host. It is not a deployed
  manager.

The `summary.json` qualification keeps `productionAuthority` and
`productionEquivalence` false.

## Review follow-up: suspension and reconcile evidence

Fault injection patches `spec.suspend` with field manager
`fleet-fault-injection`, rather than a `kubectl`-prefixed manager whose fields
Flux may take over. Before resuming, the harness requires the same suspended
object UID and generation, zero Deployment replicas and no matching Pods.
`flux-partition.json` and `flux-reflection-suspension.json` retain those checks.
This prevents a source reconciliation silently ending the injected fault.
While consumer A is still suspended, `partition-readiness` also forces the
platform's source and root sync to reconcile the dispatch revision (r3) with
fresh request tokens. The guard then records the root's applied revision
(`rootReconciledDuringFault`) beside the unchanged suspension receipt.

Convergence waits require the source and each requested Kustomization to report
`status.lastHandledReconcileAt` equal to its fresh request token, as well as
current readiness and revision. Restart checks additionally require those
receipts from both HelmReleases. `flux-reconciles.json` and `flux-restart.json`
record the acknowledgements. Old Ready status is insufficient.

A parent Kustomization's server-side apply can remove a `kubectl annotate`
request from the child it manages before any reconcile handles it. A root sync
also applies its own object. In the first fresh run, the cell root lost its only
request this way and the wait timed out. When the request annotation is gone
and the object has not acknowledged it, the wait now issues a new token and
requires that exact token to be acknowledged. `lostRequestsReissued` counts
these per object. A reissue never accepts an older acknowledgement.

**Bootstrap gating evidence.** The `flux-platform` wait samples the cell and
the hub on every poll (`flux.py wait --observe`), not only the cluster it is
waiting on. Observations are keyed by whether the dependent had already applied
the revision. `flux-verify` requires a `DependencyNotReady` observation on both
the cell and the hub with nothing applied: no applied revision and an empty
inventory. A dependent seen gated *after* applying r1 means a dependency
re-reconciled at the same revision on a fresh request. It is recorded as
`bootstrapRegatedAfterApply` and is neither evidence nor a fault. An inventory
with no applied revision, or another applied revision during bootstrap, fails.
The earlier check sampled the hub only after the cell converged. It treated any
gated observation with r1 applied as "applied before its dependency was Ready",
which it cannot show. The second fresh run of this change,
`/tmp/fleet-multiplane.XjeuF8`, failed on that false positive: its first hub
sample came after the hub had applied r1. That run had created no allocation,
handoff or effect, and was discarded with `teardown.sh --discard-unknown`.

The [fresh acceptance run](multiplane.md#flux-acknowledgement-acceptance-2026-11-15)
established these stronger claims. They supersede the
[earlier run's](multiplane.md#flux-reconciled-acceptance-2026-11-15) weaker
suspension and restart assertions. That run established PXE results and final
convergence, but not that consumer suspension persisted or that every object
reconciled after the controller restart.
