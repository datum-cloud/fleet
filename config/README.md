# Fleet deployment manifests

The customer-facing service is **Bare Metal**. The Milo bundle registers one
Service and ServiceConfiguration named `metal`, with identity
`metal.datumapis.com` and quota resource type `metal.datumapis.com/machines`.
Both catalog resources remain Draft. No separate service is registered for
`fleet.datumapis.com` or `coordination.fleet.datumapis.com`. Environment-owned
entitlements, ServiceAvailability resources, and publication rules must reference
`metal`. See the [API architecture](../docs/architecture/README.md) for group separation
and breaking alpha rename constraints.

The repository publishes two production manifest roots. They target different
API servers and must be reconciled independently.

| Root | Destination | Contents |
| --- | --- | --- |
| `config/milo` | Milo root control plane | Fleet CRDs, IAM protected resources and roles, service registration, and machine quota registration |
| `config/overlays/management-plane` | Fleet management cluster | Fleet CRDs, the manager workload and RBAC, private-record namespaces, and admission fences |
| `config/overlays/synthetic-staging` | Fleet management cluster (non-production only) | Management-plane resources plus an explicit synthetic allocator/provider integration |

The secure machine-management prototype additionally applies
`config/federation` to the Karmada API. It contains the projected-session status
interpreter and least-privilege hub RBAC; the generated projected-session CRD is
applied to the Karmada API separately. This component is not yet part of either
published production root because environment-owned Karmada identity and member
registration have not been selected. Its public session and private binding CRDs
use `metal.datumapis.com`; the projected-session CRD and hub RBAC use
`coordination.fleet.datumapis.com`. The prototype RBAC grants session creation
separately from Machine viewing. It adds no Milo service, session IAM registration,
or production release integration. See the [prototype integration gaps](../docs/implementation/machine-management-prototype.md#prototype-choices-and-remaining-production-gaps).

`config/standalone` remains a synthetic, same-cluster test environment. It must
not be referenced by either production root.

`config/overlays/synthetic-staging` is the temporary Milo-routed acceptance
environment. It uses the real private custody, exclusivity, cleanup, and reuse
implementation, but substitutes synthetic admission and provider evidence. It
accepts only powered-off, networkless claims in `us-central-1`; its status
conditions identify that boundary. When the three publication keys documented
below are present, it watches one consumer project and one distinct provider
project, publishes a locked class reference and exact profile copy, and retains
both project lifetimes in allocation custody. Provider Machine events wake
consumer claims directly; no polling resync is enabled.

The simulator runs in a separate `fleet-synthetic-provider` Deployment behind a
ClusterIP Service. The manager uses Service DNS and mounts the same dedicated
runtime credential as the provider. The environment must supply the
`fleet-system/fleet-synthetic-provider-auth` Secret with a `token` key; the
staging infra overlay generates it through External Secrets with `CreatedOnce`.
Neither process uses a Kubernetes API token to authenticate provider requests.
The provider has its own service account with token automount disabled and no
RBAC grants. Its NetworkPolicy permits runtime ingress only from manager pods
in the same namespace and denies egress. The runtime uses cluster-local HTTP;
this is a synthetic staging boundary, not a production transport-security design.
The admin listener is disabled.

The provider keeps its operation log on a retained 1 Gi ReadWriteOnce PVC using
the cluster's default StorageClass. It runs one replica with a `Recreate` strategy:
its file store is not safe for multiple writers. Do not scale it above one or
force-delete a running provider pod. Manager rollouts do not restart the provider
or discard its state. Provider rollouts briefly interrupt runtime calls; private
Fleet authority and the persisted simulator log retain exact inputs and outcomes.
Both mounts read the credential from the projected Secret directory, allowing
rotation without a restart; projection skew may temporarily reject requests.
This overlay must never be promoted to production.

### Moving from the colocated simulator

Deploy the environment's generated credential before publishing this overlay.
Stop the old manager and verify all synthetic allocations and private custody
have completed release before replacing the sidecar. Do not treat missing public
Machines as proof of cleanup. The new PVC starts empty: this change does not copy
the sidecar's ephemeral state and must not be used to abandon active operations.
After rollout, verify both Deployments, Service routing, PVC binding, and a fresh
allocation and verified release. Check provider restart recovery with an active
synthetic claim before treating persistent-state recovery as verified in staging.
Do not delete the PVC as a recovery shortcut.


Render both production roots before publishing the Kustomize bundle:

```sh
kustomize build config/milo >/dev/null
kustomize build config/overlays/management-plane >/dev/null
kustomize build config/overlays/synthetic-staging >/dev/null
```

## Published artifacts

`.github/workflows/publish.yml` publishes the multi-architecture manager image
as `ghcr.io/datum-cloud/fleet` and then publishes `config` as
`ghcr.io/datum-cloud/fleet-kustomize`. The bundle publisher stamps the exact
manager image tag into `config/base/manager`, so an environment consumes one
matched image and manifest revision.

The manager image can be built locally with:

```sh
docker build -f build/manager.Dockerfile .
```

Publishing an image does not relax the readiness boundary below. Deployment
owners must select an immutable workflow-produced tag or digest and must not
promote the Fleet Service while the production integration remains fail-closed.

## Environment-owned inputs

The management-plane root intentionally does not contain a
`fleet-manager-config` ConfigMap. The environment overlay must create that
ConfigMap in `fleet-system` with these exact keys:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: fleet-manager-config
  namespace: fleet-system
data:
  authorityID: <configured Milo authority ID>
  projectName: <Milo Project name>
```

The non-production `synthetic-staging` overlay additionally requires one
explicit publication contract:

```yaml
data:
  providerProjectName: <distinct provider Milo Project name>
  publishedClassName: <provider MachineClass definition name>
  publishedProfileName: <allowed provider MachineProfile name>
```

The publisher refuses name collisions, reference chains, mutable profile-copy
drift, a profile not allowed by the source class, or replacement source/project
lifetimes. It writes only a consumer class containing `spec.source`, an exact
profile copy, and sanitized `resolvedClass` status. These checks exercise the
publication and shared-allocation shapes, but the resulting conditions remain
explicitly synthetic authority rather than production IAM/catalog evidence.

These values select the Milo authority and project but do not duplicate the
project's immutable identity. At startup the manager reads the named Project
through the root Milo credential, rejects missing, deleting, or UID-less
projects, and retains the UID returned by Milo in its internal authority and
custody records. A missing ConfigMap prevents the Pod from starting; empty or
unresolvable values make the binary exit. The
environment overlay must also replace `ghcr.io/datum-cloud/fleet:latest` with an
immutable digest or release tag. It must provide a
`milo-configuration-kubeconfig` Secret in `fleet-system` whose `value` key is a
root Milo kubeconfig with permission to get the named Project. The manager
copies that configuration and scopes its host to
`/apis/resourcemanager.miloapis.com/v1alpha1/projects/<projectName>/control-plane`;
it never uses the root endpoint as a project cache. Kubeconfigs,
service-account tokens, provider credentials, and rendered Secrets remain owned
by `infra` and must not be added to this repository.

Fleet, NSO, quota, entitlement, Namespace, and Event access goes through the
routed Milo project credential. The management-cluster service account has no
cluster-wide role. It receives only leader-election access in `fleet-system`
and Secret access in `fleet-system-private`; the separate host cache is confined
to that private namespace. Cache scoping is not an authorization boundary. The
private namespace and `fleet-system` have Flux pruning disabled so uninstalling
one release cannot implicitly delete retained lifecycle authority.

## Current readiness boundary

The manifests package the implemented API and security boundaries; they do not
turn the manager scaffold into a production controller. The current binary
deliberately reports `/readyz` as unavailable and performs no lifecycle or
provider effects until caller-preserving admission, a live Project verifier,
durable Milo event adapters, and production handlers are composed. The
deployment is therefore suitable for render, policy, and fail-closed rollout
validation, not for claiming a functional allocation service.

Provider inventory, MachineClasses, MachineProfiles, project-specific role
bindings, quota grants, service entitlements, and publication selection are
environment or provider state. They are not installed by the production roots.
The synthetic overlay publishes only the one explicitly named staging class and
profile; it is not a general production catalog publisher. In
particular, the proposal's `ServiceConfiguration.spec.provisioning` example is
explicitly incomplete and source-project-specific, so this bundle does not
invent a publication source or claim that authorization integration exists.
The Fleet Service is registered in `Draft`; promotion to `Published` is a later,
forward-only rollout decision after the production integration and live
acceptance gates pass.

## Metal quota registration migration

The Metal allocation quota uses a new ResourceRegistration named `metal-machines`.
Milo makes `spec.resourceType` immutable, so the existing `fleet-machines`
registration cannot be retargeted. The bundle retains that legacy registration
with `fleet.datumapis.com/machines` and disables Flux pruning for it. This is a
quota compatibility record, not a second Service registration. Do not remove it
until old grants, claims, and accounting obligations have settled.

Coordinate environment changes before publishing the corrected bundle: publication
patches must target the `metal` Service and ServiceConfiguration, provider inventory
must use `metal.datumapis.com/v1alpha1`, location availability must reference
`metal`, and staff bindings must reference the Metal IAM roles. Gate the manager
on inventory and service availability as well as the Milo configuration. Confirm
old allocations are released immediately before rollout; absence of public
Machines alone does not prove private cleanup is complete.
