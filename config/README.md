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
bindings, quota grants, service entitlements, and publication objects are
environment or provider state. They are not installed by these roots. In
particular, the proposal's `ServiceConfiguration.spec.provisioning` example is
explicitly incomplete and source-project-specific, so this bundle does not
invent a publication source or claim that authorization integration exists.
The Fleet Service is registered in `Draft`; promotion to `Published` is a later,
forward-only rollout decision after the production integration and live
acceptance gates pass.
