#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT

cat >"$temporary/kind" <<'EOF'
#!/bin/sh
set -eu
cat <<'KUBECONFIG'
apiVersion: v1
clusters:
- cluster:
    server: https://127.0.0.1:6443
  name: kind-fleet-core
contexts:
- context:
    cluster: kind-fleet-core
  name: kind-fleet-core
current-context: kind-fleet-core
kind: Config
KUBECONFIG
EOF
cat >"$temporary/kubectl" <<'EOF'
#!/bin/sh
set -eu
case " $* " in
  *" config view "*) printf '%s' 'https://127.0.0.1:6443' ;;
  *" get deployments -A "*) printf 'unrelated-system %s\n' "$MANAGER_NAME" ;;
  *) exit 99 ;;
esac
EOF
cat >"$temporary/docker" <<EOF
#!/bin/sh
touch "$temporary/docker-was-called"
exit 98
EOF
chmod +x "$temporary/kind" "$temporary/kubectl" "$temporary/docker"

for manager in fleet-manager fleet-milo-manager; do
  rm -f "$temporary/docker-was-called"
  if (cd "$root" && MANAGER_NAME="$manager" PATH="$temporary:$PATH" ./hack/standalone-smoke.sh >"$temporary/output" 2>&1); then
    echo "standalone smoke accepted a cluster with a $manager deployment" >&2
    exit 1
  fi
  if ! grep -q "manager deployment unrelated-system/$manager already exists" "$temporary/output"; then
    cat "$temporary/output" >&2
    echo "standalone smoke did not report the $manager preflight failure" >&2
    exit 1
  fi
  if [ -e "$temporary/docker-was-called" ]; then
    echo "standalone smoke reached its build step before rejecting $manager" >&2
    exit 1
  fi
done
grep -q 'fleet-manager.*fleet-milo-manager' "$root/docs/operations/standalone-smoke.md"

cat >"$temporary/docker" <<'EOF'
#!/bin/sh
exit 0
EOF
cat >"$temporary/kustomize" <<'EOF'
#!/bin/sh
set -eu
case "$*" in
  # The smoke installs the Metal, inventory and Device custody CRDs
  # together, and every one must be Established before any fixture.
  'build config/base/crd' | 'build config/components/inventory/crds' | 'build config/components/device-operations/crds')
    touch "$ORDER_DIRECTORY/built-$(echo "$2" | tr / -)"
    echo 'kind: CustomResourceDefinition'
    ;;
  'build config/standalone')
    test -f "$ORDER_DIRECTORY/established"
    echo 'kind: Installation'
    ;;
  *) exit 99 ;;
esac
EOF
cat >"$temporary/kubectl" <<'EOF'
#!/bin/sh
set -eu
case " $* " in
  *" config view "*) printf '%s' 'https://127.0.0.1:6443' ;;
  *" get deployments -A "*) exit 0 ;;
  *" get "*) exit 1 ;;
  *" apply -f "*)
    for argument do manifest=$argument; done
    if grep -q 'CustomResourceDefinition' "$manifest"; then
      for crds in config-base-crd config-components-inventory-crds config-components-device-operations-crds; do
        test -f "$ORDER_DIRECTORY/built-$crds"
      done
      touch "$ORDER_DIRECTORY/crds-applied"
    else
      test -f "$ORDER_DIRECTORY/established"
      grep -q 'Installation' "$manifest"
      touch "$ORDER_DIRECTORY/fixtures-applied"
    fi
    ;;
  *" wait --for=condition=Established "*)
    test -f "$ORDER_DIRECTORY/crds-applied"
    test "$ESTABLISHMENT_RESULT" = success
    touch "$ORDER_DIRECTORY/established"
    ;;
  *" rollout status "*) echo 'ordering verified'; exit 97 ;;
  *) exit 99 ;;
esac
EOF
chmod +x "$temporary/kustomize"
for result in success failure; do
  order_directory="$temporary/$result"
  mkdir "$order_directory"
  if (cd "$root" && ORDER_DIRECTORY="$order_directory" ESTABLISHMENT_RESULT="$result" PATH="$temporary:$PATH" ./hack/standalone-smoke.sh >"$temporary/output" 2>&1); then
    echo 'ordering fixture unexpectedly completed a live smoke' >&2
    exit 1
  fi
  test -f "$order_directory/crds-applied"
  if [ "$result" = success ]; then
    test -f "$order_directory/fixtures-applied"
    grep -q 'ordering verified' "$temporary/output"
  else
    test ! -e "$order_directory/fixtures-applied"
  fi
done
