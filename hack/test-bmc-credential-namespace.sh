#!/bin/sh
# Proves hack/validate-bmc-credential-namespace.sh passes the real config tree
# and fails when a Secret that is not a BMC credential Secret renders into a
# BMC credential namespace.
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
work_root=$(mktemp -d)
fixture_root="$work_root/path with spaces"
trap 'rm -rf "$work_root"' EXIT

validate() {
  "$repo_root/hack/validate-bmc-credential-namespace.sh" "$1" >"$work_root/output" 2>&1
}

expect_pass() {
  if ! validate "$2"; then
    echo "$1 was rejected:" >&2
    cat "$work_root/output" >&2
    exit 1
  fi
}

expect_failure() {
  description=$1
  expected=$2
  if validate "$3"; then
    echo "$description was not rejected" >&2
    exit 1
  fi
  if ! grep -F "$expected" "$work_root/output" >/dev/null; then
    echo "$description failed for the wrong reason:" >&2
    cat "$work_root/output" >&2
    exit 1
  fi
}

stray_secret() {
  cat <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: $1
  namespace: ${2:-}
stringData: {token: synthetic-not-a-credential}
EOF
}

credential_secret() {
  cat <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: $1
  labels: {fleet.datumapis.com/bmc-credential: "true"}
  annotations:
    fleet.datumapis.com/device-project: synthetic-provider
    fleet.datumapis.com/device-namespace: fleet-provider
    fleet.datumapis.com/device-name: synthetic-device-01
stringData: {username: synthetic-user, password: synthetic-password}
EOF
}

# The real tree passes, and a copy of it still passes before each mutation.
expect_pass "the real config tree" "$repo_root/config"
real="$fixture_root/real"
mkdir -p "$real"
cp -R "$repo_root/config" "$real/config"
expect_pass "the copied config tree" "$real/config"
grep -F "base/bmc-discovery" "$work_root/output" >/dev/null
grep -F "site/redfish/base" "$work_root/output" >/dev/null

# A stray Secret in discovery's base lands in whatever namespace the
# deploying repository chooses for discovery.
discovery="$real/config/base/bmc-discovery"
cp "$discovery/kustomization.yaml" "$work_root/discovery-kustomization"
stray_secret synthetic-stray >"$discovery/stray-secret.yaml"
awk '{ print } /^  - deployment.yaml$/ { print "  - stray-secret.yaml" }' "$work_root/discovery-kustomization" >"$discovery/kustomization.yaml"
if cmp -s "$work_root/discovery-kustomization" "$discovery/kustomization.yaml"; then
  echo "discovery stray Secret fixture did not change the Kustomization" >&2
  exit 1
fi
expect_failure "stray Secret in the discovery base" 'base/bmc-discovery: Secret "synthetic-stray" in BMC credential namespace (set by the deploying repository) is not a credential Secret' "$real/config"
mv "$work_root/discovery-kustomization" "$discovery/kustomization.yaml"
rm "$discovery/stray-secret.yaml"

# A generated Secret is checked after generation, under its hashed name.
cp "$discovery/kustomization.yaml" "$work_root/discovery-kustomization"
cat >>"$discovery/kustomization.yaml" <<'EOF'
secretGenerator:
  - name: synthetic-generated
    literals: [token=synthetic-not-a-credential]
EOF
expect_failure "generated Secret in the discovery base" 'Secret "synthetic-generated-' "$real/config"
mv "$work_root/discovery-kustomization" "$discovery/kustomization.yaml"

# A stray Secret in the site's credential namespace.
site="$real/config/site/redfish/base"
cp "$site/credentials.yaml" "$work_root/site-credentials"
{ echo '---'; stray_secret synthetic-site-stray fleet-site-credentials; } >>"$site/credentials.yaml"
expect_failure "stray Secret in the site credential namespace" 'site/redfish/base: Secret "synthetic-site-stray" in BMC credential namespace fleet-site-credentials is not a credential Secret' "$real/config"
mv "$work_root/site-credentials" "$site/credentials.yaml"
expect_pass "the restored config tree" "$real/config"

# A Secret added to the site credential namespace from another root, or from
# a Component rendered on its own, is still in that namespace once applied.
mkdir -p "$real/config/multiplane/stray" "$real/config/components/stray"
stray_secret synthetic-cross-root fleet-site-credentials >"$real/config/multiplane/stray/secret.yaml"
cat >"$real/config/multiplane/stray/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [secret.yaml]
EOF
expect_failure "stray Secret added to the site credential namespace from another root" 'multiplane/stray: Secret "synthetic-cross-root" in BMC credential namespace fleet-site-credentials' "$real/config"
rm -rf "$real/config/multiplane/stray"
stray_secret synthetic-component fleet-site-credentials >"$real/config/components/stray/secret.yaml"
cat >"$real/config/components/stray/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
resources: [secret.yaml]
EOF
expect_failure "stray Secret added to the site credential namespace by a Component" 'components/stray: Secret "synthetic-component" in BMC credential namespace fleet-site-credentials' "$real/config"
rm -rf "$real/config/components/stray"
expect_pass "the restored config tree" "$real/config"

# Synthetic roots: a namespaced discovery overlay, a labelled namespace and
# the credential contract itself.
synthetic="$fixture_root/synthetic/config"
mkdir -p "$synthetic/base/bmc-discovery" "$synthetic/overlays/discovery"
cp "$repo_root/config/base/bmc-discovery/rbac.yaml" "$synthetic/base/bmc-discovery/"
cat >"$synthetic/base/bmc-discovery/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [rbac.yaml]
EOF
cat >"$synthetic/overlays/discovery/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: synthetic-bmc-credentials
resources: [../../base/bmc-discovery, credential.yaml]
EOF
credential_secret synthetic-credential >"$synthetic/overlays/discovery/credential.yaml"
expect_pass "a namespaced discovery overlay with a credential Secret" "$synthetic"

stray_secret synthetic-overlay-stray >"$synthetic/overlays/discovery/stray.yaml"
sed -i.bak 's/credential.yaml\]/credential.yaml, stray.yaml]/' "$synthetic/overlays/discovery/kustomization.yaml"
expect_failure "stray Secret in a namespaced discovery overlay" 'overlays/discovery: Secret "synthetic-overlay-stray" in BMC credential namespace synthetic-bmc-credentials is not a credential Secret' "$synthetic"
mv "$synthetic/overlays/discovery/kustomization.yaml.bak" "$synthetic/overlays/discovery/kustomization.yaml"
rm "$synthetic/overlays/discovery/stray.yaml"

# Discovery's own Milo kubeconfig may sit beside discovery, and nowhere else.
stray_secret fleet-bmc-discovery-milo-kubeconfig >"$synthetic/overlays/discovery/kubeconfig.yaml"
sed -i.bak 's/credential.yaml\]/credential.yaml, kubeconfig.yaml]/' "$synthetic/overlays/discovery/kustomization.yaml"
expect_pass "discovery's Milo kubeconfig beside discovery" "$synthetic"
mv "$synthetic/overlays/discovery/kustomization.yaml.bak" "$synthetic/overlays/discovery/kustomization.yaml"
rm "$synthetic/overlays/discovery/kubeconfig.yaml"

# A Secret elsewhere in the same render is not discovery's concern.
mkdir -p "$synthetic/overlays/elsewhere"
stray_secret synthetic-elsewhere synthetic-other >"$synthetic/overlays/elsewhere/stray.yaml"
cat >"$synthetic/overlays/elsewhere/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [../discovery, stray.yaml]
EOF
expect_pass "a Secret outside the credential namespace" "$synthetic"
rm -rf "$synthetic/overlays/elsewhere"

# A credential Secret must name its Device.
credential_secret synthetic-unnamed | grep -v 'device-name:' >"$synthetic/overlays/discovery/credential.yaml"
expect_failure "credential Secret naming no Device" 'lacks annotation fleet.datumapis.com/device-name' "$synthetic"
credential_secret synthetic-credential | sed 's/bmc-credential: "true"/bmc-credential: "false"/' >"$synthetic/overlays/discovery/credential.yaml"
expect_failure "credential label set to false" 'lacks label fleet.datumapis.com/bmc-credential: "true"' "$synthetic"
credential_secret synthetic-credential >"$synthetic/overlays/discovery/credential.yaml"

# A labelled Namespace is a credential namespace without discovery in it.
mkdir -p "$synthetic/overlays/labelled"
cat >"$synthetic/overlays/labelled/resources.yaml" <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: synthetic-site-credentials
  labels: {fleet.datumapis.com/bmc-credential-namespace: "true"}
---
apiVersion: v1
kind: Secret
metadata: {name: synthetic-labelled-stray, namespace: synthetic-site-credentials}
stringData: {token: synthetic-not-a-credential}
EOF
cat >"$synthetic/overlays/labelled/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [resources.yaml]
EOF
expect_failure "stray Secret in a labelled namespace" 'overlays/labelled: Secret "synthetic-labelled-stray" in BMC credential namespace synthetic-site-credentials' "$synthetic"
cat >"$synthetic/overlays/labelled/resources.yaml" <<'EOF'
apiVersion: v1
kind: Namespace
metadata:
  name: synthetic-site-credentials
  labels: {fleet.datumapis.com/bmc-credential-namespace: "true"}
---
apiVersion: v1
kind: Secret
metadata: {name: fleet-bmc-discovery-milo-kubeconfig, namespace: synthetic-site-credentials}
stringData: {kubeconfig: synthetic}
EOF
expect_failure "discovery's kubeconfig name in a namespace without discovery" 'Secret "fleet-bmc-discovery-milo-kubeconfig" in BMC credential namespace synthetic-site-credentials' "$synthetic"
rm -rf "$synthetic/overlays/labelled"

# A tree with no credential namespace checked nothing, and says so.
rm -rf "$synthetic/base/bmc-discovery" "$synthetic/overlays/discovery"
mkdir -p "$synthetic/overlays/empty"
cat >"$synthetic/overlays/empty/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
EOF
expect_failure "a tree without a credential namespace" "renders a BMC credential namespace" "$synthetic"

echo "BMC credential namespace check: all cases passed"
