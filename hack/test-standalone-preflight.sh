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
grep -q 'fleet-manager.*fleet-milo-manager' "$root/docs/standalone-smoke.md"
