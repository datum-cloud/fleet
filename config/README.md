# Fleet deployment manifests

The repository publishes two production manifest roots. They target different
API servers and must be reconciled independently.

| Root | Destination | Contents |
| --- | --- | --- |
| `config/milo` | Milo root control plane | Fleet CRDs, IAM protected resources and roles, service registration, and machine quota registration |
| `config/overlays/management-plane` | Fleet management cluster | Fleet CRDs, the manager workload and RBAC, private-record namespaces, and admission fences |

`config/standalone` remains a synthetic, same-cluster test environment. It must
not be referenced by either production root.

Render both production roots before publishing the Kustomize bundle:

```sh
kustomize build config/milo >/dev/null
kustomize build config/overlays/management-plane >/dev/null
```

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
  projectUID: <exact root Project UID>
  projectRoute: <Milo project route name>
```

These values are authority, not portable defaults. A missing ConfigMap prevents
the Pod from starting; empty or inconsistent values make the binary exit. The
environment overlay must also replace `ghcr.io/datum-cloud/fleet:latest` with an
immutable digest or release tag. Kubeconfigs, service-account tokens, provider
credentials, and rendered Secrets remain owned by `infra` and must not be added
to this repository.

The manager receives cluster-wide access only to the watched Fleet, NSO, quota,
entitlement, Namespace, and Event surfaces. Its retained authority records are
Secrets, and write access to them is confined to the `fleet-system-private`
namespace by a namespaced Role. That namespace and `fleet-system` have Flux
pruning disabled so uninstalling one release cannot implicitly delete retained
lifecycle authority.

## Current readiness boundary

The manifests package the implemented API and security boundaries; they do not
turn the manager scaffold into a production controller. The current binary
deliberately reports `/readyz` as unavailable and performs no lifecycle or
provider effects until caller-preserving admission, a live Project verifier,
durable Milo event adapters, and production handlers are composed. The
deployment is therefore suitable for render, policy, and fail-closed rollout
validation, not for claiming a functional allocation service.

Provider inventory, MachineClasses, MachineProfiles, project-specific role
bindings, quota grants, service entitlements, and publication objects are
environment or provider state. They are not installed by these roots. In
particular, the proposal's `ServiceConfiguration.spec.provisioning` example is
explicitly incomplete and source-project-specific, so this bundle does not
invent a publication source or claim that authorization integration exists.
The Fleet Service is registered in `Draft`; promotion to `Published` is a later,
forward-only rollout decision after the production integration and live
acceptance gates pass.
