#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
work_root=$(mktemp -d)
fixture_root="$work_root/path with spaces"
trap 'rm -rf "$work_root"' EXIT

expect_failure() {
  description=$1
  expected=${2:-}
  if "$repo_root/hack/validate-overlay-isolation.sh" "$fixture_root/config" "$fixture_root/config/standalone" >"$work_root/output" 2>&1; then
    echo "$description was not rejected" >&2
    exit 1
  fi
  if [ -n "$expected" ] && ! grep -F "$expected" "$work_root/output" >/dev/null; then
    echo "$description failed for the wrong reason:" >&2
    cat "$work_root/output" >&2
    exit 1
  fi
}

expect_remote_base_failure() {
  name=$1
  reference=$2
  directory="$fixture_root/config/overlays/$name"
  mkdir -p "$directory"
  {
    echo 'apiVersion: kustomize.config.k8s.io/v1beta1'
    echo 'kind: Kustomization'
    printf 'bases: [%s]\n' "\"$reference\""
  } >"$directory/kustomization.yaml"
  expect_failure "$name remote base" "not a plain local relative path"
  rm -rf "$directory"
}

mkdir -p "$fixture_root/config/base/safe" "$fixture_root/config/base/simulator" "$fixture_root/config/standalone" "$fixture_root/config/overlays/production"
cat >"$fixture_root/config/base/safe/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [configmap.yaml]
configMapGenerator:
  - name: generated-production-config
    literals:
      - note=standalone-synthetic-runtime-token-not-for-production fleet-standalone-fixture-reader
EOF
cat >"$fixture_root/config/base/safe/configmap.yaml" <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata: {name: production-safe}
data:
  benign-text: fleet-simulator-runtime central1-baremetal-01
EOF
cat >"$fixture_root/config/base/simulator/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [service.yaml]
EOF
cat >"$fixture_root/config/base/simulator/service.yaml" <<'EOF'
apiVersion: v1
kind: Service
metadata:
  name: fleet-simulator
  annotations: {fleet.datumapis.com/standalone-only: "true"}
spec: {ports: [{port: 8080}]}
EOF
cat >"$fixture_root/config/standalone/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../base/simulator, namespaces.yaml, runtime-secret.yaml, rbac.yaml]
secretGenerator:
  - name: generated-runtime
    literals: [token=generated-standalone-token]
generatorOptions:
  annotations: {fleet.datumapis.com/standalone-only: "true"}
EOF
cat >"$fixture_root/config/standalone/namespaces.yaml" <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: fleet-system
  annotations:
    fleet.datumapis.com/standalone-only: "true"
    fleet.datumapis.com/shared-with-standalone: "true"
---
apiVersion: v1
kind: Namespace
metadata:
  name: fleet-project-a
  labels: {fleet.datumapis.com/local-project-stand-in: "true"}
  annotations: {fleet.datumapis.com/standalone-only: "true"}
EOF
cat >"$fixture_root/config/standalone/runtime-secret.yaml" <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: fleet-simulator-runtime
  annotations: {fleet.datumapis.com/standalone-only: "true"}
stringData: {token: standalone-synthetic-runtime-token-not-for-production}
EOF
cat >"$fixture_root/config/standalone/rbac.yaml" <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: fleet-standalone-fixture-reader
  annotations: {fleet.datumapis.com/standalone-only: "true"}
rules:
  - apiGroups: [metal.datumapis.com]
    resources: [machineprofiles, machineclasses]
    resourceNames: [general-purpose-24c-128g, standard-metal]
    verbs: [get, list]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: fleet-standalone-fixture-reader
  annotations: {fleet.datumapis.com/standalone-only: "true"}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: fleet-standalone-fixture-reader}
subjects: [{kind: ServiceAccount, name: fleet-standalone-smoke, namespace: fleet-system}]
EOF
cat >"$fixture_root/config/overlays/production/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../../base/safe]
EOF

"$repo_root/hack/validate-overlay-isolation.sh" "$fixture_root/config" "$fixture_root/config/standalone"

# A direct raw file reference into the protected tree must fail before rendering.
mkdir -p "$fixture_root/config/overlays/direct-file"
cat >"$fixture_root/config/overlays/direct-file/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../../standalone/runtime-secret.yaml]
EOF
expect_failure "direct standalone file import" "source graph reaches standalone-only path"
rm -rf "$fixture_root/config/overlays/direct-file"

# Deprecated bases are resource-producing graph edges too.
mkdir -p "$fixture_root/config/overlays/deprecated-base"
cat >"$fixture_root/config/overlays/deprecated-base/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
bases: [../../base/simulator]
EOF
expect_failure "deprecated protected base" "source graph reaches standalone-only path"
rm -rf "$fixture_root/config/overlays/deprecated-base"

# Every remote grammar accepted by Kustomize fails closed before filesystem or
# network access. Query-bearing local-looking paths are remote repo specs too.
expect_remote_base_failure "url-scheme" "https://invalid.example.test/fleet/base"
expect_remote_base_failure "git-prefix" "git::https://invalid.example.test/fleet.git//base"
expect_remote_base_failure "query-ref" "../../base/safe?ref=main"
expect_remote_base_failure "query-version" "../../base/safe?version=v1.2.3"
expect_remote_base_failure "git-suffix" "invalid.example.test/fleet.git//base"
expect_remote_base_failure "scp-style" "git@invalid.example.test:fleet/base"
expect_remote_base_failure "scheme-less-github" "github.com/example/fleet//base"

# An on-disk path that shadows a remote repo spec must not change its
# classification. Kustomize would still apply repo grammar to the original text.
mkdir -p "$fixture_root/config/overlays/shadow-local/github.com/example/fleet/base"
expect_remote_base_failure "shadow-local" "github.com/example/fleet//base"

# Lexical traversal and symlink traversal are checked on opposite sides of
# filesystem resolution, so both remain confined to the audited config tree.
cat >"$fixture_root/outside.yaml" <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata: {name: outside-audited-tree}
EOF
mkdir -p "$fixture_root/config/overlays/lexical-escape"
cat >"$fixture_root/config/overlays/lexical-escape/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../../../outside.yaml]
EOF
expect_failure "lexical config-tree escape" "escapes audited config tree"
rm -rf "$fixture_root/config/overlays/lexical-escape"

mkdir -p "$fixture_root/outside-base"
cat >"$fixture_root/outside-base/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../outside.yaml]
EOF
ln -s "$fixture_root/outside-base" "$fixture_root/config/base/outside-link"
mkdir -p "$fixture_root/config/overlays/symlink-escape"
cat >"$fixture_root/config/overlays/symlink-escape/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../../base/outside-link]
EOF
expect_failure "symlink config-tree escape" "resolves outside audited config tree"
rm -rf "$fixture_root/config/overlays/symlink-escape" "$fixture_root/config/base/outside-link"

# The pinned Kustomize still accepts the legacy Helm resource generator; it is
# intentionally rejected because CI cannot prove its fetched inputs hermetic.
mkdir -p "$fixture_root/config/overlays/legacy-helm"
cat >"$fixture_root/config/overlays/legacy-helm/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmChartInflationGenerator:
  chartName: remote-chart
  chartRepoUrl: https://invalid.example.test/charts
  chartVersion: 1.0.0
  releaseName: remote-chart
EOF
expect_failure "legacy Helm resource generator" "Helm resource generation is not auditable"
rm -rf "$fixture_root/config/overlays/legacy-helm"

# Reachability remains forbidden through arbitrarily nested child Kustomizations.
mkdir -p "$fixture_root/config/base/nested/child" "$fixture_root/config/overlays/transitive"
cat >"$fixture_root/config/base/nested/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [child]
EOF
cat >"$fixture_root/config/base/nested/child/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../../../base/simulator/service.yaml]
EOF
cat >"$fixture_root/config/overlays/transitive/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../../base/nested]
EOF
expect_failure "nested transitive simulator import" "source graph reaches standalone-only path"
rm -rf "$fixture_root/config/base/nested" "$fixture_root/config/overlays/transitive"

# Components participate in the same canonical source graph as resources.
mkdir -p "$fixture_root/config/base/component-leak" "$fixture_root/config/overlays/component-leak"
cat >"$fixture_root/config/base/component-leak/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources: [../simulator/service.yaml]
EOF
cat >"$fixture_root/config/overlays/component-leak/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../../base/safe]
components: [../../base/component-leak]
EOF
expect_failure "transitive simulator component import" "source graph reaches standalone-only path"
rm -rf "$fixture_root/config/base/component-leak" "$fixture_root/config/overlays/component-leak"

# Every standalone output object must retain the exact boolean-string marker.
cp "$fixture_root/config/standalone/kustomization.yaml" "$work_root/standalone-kustomization"
cat >"$fixture_root/config/standalone/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../base/simulator, namespaces.yaml, runtime-secret.yaml, rbac.yaml, unmarked.yaml]
secretGenerator:
  - name: generated-runtime
    literals: [token=generated-standalone-token]
generatorOptions:
  annotations: {fleet.datumapis.com/standalone-only: "true"}
EOF
cat >"$fixture_root/config/standalone/unmarked.yaml" <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata: {name: selectively-unmarked}
EOF
expect_failure "selectively removed standalone marker" "must have fleet.datumapis.com/standalone-only"
mv "$work_root/standalone-kustomization" "$fixture_root/config/standalone/kustomization.yaml"
rm "$fixture_root/config/standalone/unmarked.yaml"

# Namespace fixture identity cannot be laundered by copying or renaming the
# raw standalone file outside its source graph.
mkdir -p "$fixture_root/config/overlays/renamed-stand-in"
cat >"$fixture_root/config/overlays/renamed-stand-in/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [namespace.yaml]
EOF
cat >"$fixture_root/config/overlays/renamed-stand-in/namespace.yaml" <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: completely-renamed-project
  labels: {fleet.datumapis.com/local-project-stand-in: "true"}
EOF
expect_failure "renamed standalone project namespace" "standalone local-project stand-in identity"
rm -rf "$fixture_root/config/overlays/renamed-stand-in"

mkdir -p "$fixture_root/config/overlays/copied-namespace-name"
cat >"$fixture_root/config/overlays/copied-namespace-name/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [namespace.yaml]
EOF
cat >"$fixture_root/config/overlays/copied-namespace-name/namespace.yaml" <<'EOF'
apiVersion: v1
kind: Namespace
metadata: {name: fleet-system}
EOF
expect_failure "copied standalone namespace name" "duplicates a standalone-only namespace name"
rm -rf "$fixture_root/config/overlays/copied-namespace-name"

mkdir -p "$fixture_root/config/overlays/shared-system-namespace"
cat >"$fixture_root/config/overlays/shared-system-namespace/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [namespace.yaml]
EOF
cat >"$fixture_root/config/overlays/shared-system-namespace/namespace.yaml" <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: fleet-system
  annotations: {fleet.datumapis.com/shared-with-standalone: "true"}
EOF
"$repo_root/hack/validate-overlay-isolation.sh" "$fixture_root/config" "$fixture_root/config/standalone"
rm -rf "$fixture_root/config/overlays/shared-system-namespace"

# Secret.data is decoded and names are irrelevant.
mkdir -p "$fixture_root/config/overlays/base64-secret"
cat >"$fixture_root/config/overlays/base64-secret/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [secret.yaml]
EOF
cat >"$fixture_root/config/overlays/base64-secret/secret.yaml" <<'EOF'
apiVersion: v1
kind: Secret
metadata: {name: completely-renamed}
data: {credential: c3RhbmRhbG9uZS1zeW50aGV0aWMtcnVudGltZS10b2tlbi1ub3QtZm9yLXByb2R1Y3Rpb24=}
EOF
expect_failure "base64 standalone token"
rm -rf "$fixture_root/config/overlays/base64-secret"

# Kustomize-generated Secrets are checked after generation and decoding.
mkdir -p "$fixture_root/config/overlays/generated-secret"
cat >"$fixture_root/config/overlays/generated-secret/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
secretGenerator:
  - name: unrelated-name
    literals: [credential=prefix-generated-standalone-token-suffix]
EOF
expect_failure "secretGenerator standalone token"
rm -rf "$fixture_root/config/overlays/generated-secret"

# A complete RBAC alias still has the standalone fixture's authority shape.
mkdir -p "$fixture_root/config/overlays/rbac-alias"
cat >"$fixture_root/config/overlays/rbac-alias/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [rbac.yaml]
EOF
cat >"$fixture_root/config/overlays/rbac-alias/rbac.yaml" <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: innocuous-reader}
rules:
  - apiGroups: [metal.datumapis.com]
    resources: [machineprofiles]
    resourceNames: [renamed-profile, renamed-profile]
    verbs: [get, get]
  - apiGroups: [metal.datumapis.com]
    resources: [machineprofiles]
    resourceNames: [duplicate-renamed-profile]
    verbs: [get]
  - apiGroups: [metal.datumapis.com, metal.datumapis.com]
    resources: [machineprofiles]
    resourceNames: [another-renamed-profile]
    verbs: [list]
  - apiGroups: [metal.datumapis.com]
    resources: [machineclasses, machineclasses]
    resourceNames: [renamed-class]
    verbs: [get, list, list]
  - apiGroups: [metal.datumapis.com]
    resources: [machineclasses]
    resourceNames: [second-renamed-class, second-renamed-class]
    verbs: [get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: innocuous-binding}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: innocuous-reader}
subjects:
  - {kind: ServiceAccount, name: unrelated-account, namespace: another-namespace}
  - {kind: ServiceAccount, name: duplicate-account, namespace: duplicate-namespace}
  - {kind: ServiceAccount, name: duplicate-account, namespace: duplicate-namespace}
EOF
expect_failure "fully aliased fixture RBAC"
rm -rf "$fixture_root/config/overlays/rbac-alias"

mkdir -p "$fixture_root/config/overlays/rbac-binding-alias"
cat >"$fixture_root/config/overlays/rbac-binding-alias/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [binding.yaml]
EOF
cat >"$fixture_root/config/overlays/rbac-binding-alias/binding.yaml" <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: wholly-different-binding}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: wholly-different-role}
subjects:
  - {kind: ServiceAccount, name: wholly-different-account, namespace: wholly-different-namespace}
  - {kind: ServiceAccount, name: duplicated-account, namespace: duplicated-namespace}
  - {kind: ServiceAccount, name: duplicated-account, namespace: duplicated-namespace}
EOF
# Binding names and subject identities are authority and are therefore compared
# exactly. Merely having the same binding shape is not fixture reuse.
"$repo_root/hack/validate-overlay-isolation.sh" "$fixture_root/config" "$fixture_root/config/standalone"
rm -rf "$fixture_root/config/overlays/rbac-binding-alias"

# A broken discovered entry point is a validation failure, never silently skipped.
mkdir -p "$fixture_root/config/overlays/broken"
cat >"$fixture_root/config/overlays/broken/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [missing.yaml]
EOF
expect_failure "intentional Kustomize build failure" "missing.yaml"
rm -rf "$fixture_root/config/overlays/broken"

"$repo_root/hack/validate-overlay-isolation.sh" "$fixture_root/config" "$fixture_root/config/standalone"
