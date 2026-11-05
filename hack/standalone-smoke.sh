#!/bin/sh
set -eu

context=${KUBECTL_CONTEXT:-kind-fleet-core}
cluster=${KIND_CLUSTER:-fleet-core}
expected_context="kind-$cluster"
if [ "$context" != "$expected_context" ] && [ "${ALLOW_KIND_CONTEXT_ALIAS:-}" != "true" ]; then
  echo "KUBECTL_CONTEXT must be $expected_context (set ALLOW_KIND_CONTEXT_ALIAS=true only for an alias of the same kind cluster)" >&2
  exit 1
fi

kind_kubeconfig=$(mktemp)
rendered=$(mktemp)
trap 'rm -f "$kind_kubeconfig" "$rendered"' EXIT
source_id=$(./hack/standalone-source-id.sh)
simulator_image="fleet-simulator:standalone-$source_id"
smoke_image="fleet-standalone-smoke:standalone-$source_id"

kind get kubeconfig --name "$cluster" >"$kind_kubeconfig"
kind_server=$(kubectl --kubeconfig "$kind_kubeconfig" config view --minify -o jsonpath='{.clusters[0].cluster.server}')
context_server=$(kubectl --context "$context" config view --minify -o jsonpath='{.clusters[0].cluster.server}')
if [ "$kind_server" != "$context_server" ]; then
  echo "context $context does not target kind cluster $cluster" >&2
  exit 1
fi

# Refuse every known production manager name before creating or changing any
# object. Namespaces are intentionally not assumed: a manager in any namespace
# could race the synthetic sole writer and invalidate the result.
manager_deployments=$(kubectl --context "$context" get deployments -A \
  -o 'custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name' --no-headers)
while read -r manager_namespace manager_name; do
  case "$manager_name" in
    fleet-manager|fleet-milo-manager)
      echo "manager deployment $manager_namespace/$manager_name already exists; use a fresh dedicated kind cluster" >&2
      exit 1
      ;;
  esac
done <<EOF
$manager_deployments
EOF

for namespace in fleet-system fleet-system-private fleet-provider fleet-project-a fleet-project-b; do
  if kubectl --context "$context" get namespace "$namespace" >/dev/null 2>&1; then
    echo "fixed namespace $namespace already exists; use a fresh dedicated kind cluster" >&2
    exit 1
  fi
done
for crd in installations machineclaims machineclasses machineprofiles machines; do
  if kubectl --context "$context" get crd "$crd.fleet.datumapis.com" >/dev/null 2>&1; then
    echo "fixed Fleet CRD $crd already exists; use a fresh dedicated kind cluster" >&2
    exit 1
  fi
done
for object in clusterrole/fleet-standalone-fixture-reader clusterrolebinding/fleet-standalone-fixture-reader; do
  if kubectl --context "$context" get "$object" >/dev/null 2>&1; then
    echo "fixed cluster object $object already exists; use a fresh dedicated kind cluster" >&2
    exit 1
  fi
done
# The five required namespaces are absent, which also proves every fixed
# namespaced ServiceAccount, Role, binding, Secret, workload, Job, and fixture
# name is absent. No ownership label or ConfigMap is accepted as resume proof.

docker build -f build/simulator.Dockerfile -t "$simulator_image" .
docker build -f build/standalone-smoke.Dockerfile -t "$smoke_image" .
kind load docker-image --name "$cluster" "$simulator_image" "$smoke_image"
kustomize build config/standalone |
  sed -e "s#fleet-simulator:standalone-local#$simulator_image#g" -e "s#fleet-standalone-smoke:standalone-local#$smoke_image#g" >"$rendered"
if grep -q 'standalone-local' "$rendered"; then
  echo "local image placeholder remained in rendered workload" >&2
  exit 1
fi
kubectl --context "$context" apply -f "$rendered"
kubectl --context "$context" -n fleet-system rollout status statefulset/fleet-simulator --timeout=120s
simulator_pod=$(kubectl --context "$context" -n fleet-system get pod -l app.kubernetes.io/name=fleet-simulator -o jsonpath='{.items[0].metadata.name}')
test "$(kubectl --context "$context" -n fleet-system get pod "$simulator_pod" -o jsonpath='{.spec.containers[0].image}')" = "$simulator_image"
test "$(kubectl --context "$context" -n fleet-system get pod "$simulator_pod" -o jsonpath='{.spec.containers[0].imagePullPolicy}')" = "Never"
test -n "$(kubectl --context "$context" -n fleet-system get pod "$simulator_pod" -o jsonpath='{.status.containerStatuses[0].imageID}')"

run_phase() {
  phase=$1
  run=$2
  kubectl --context "$context" -n fleet-system create job --from="cronjob/fleet-smoke-$phase" "$run"
  kubectl --context "$context" -n fleet-system wait --for=condition=complete "job/$run" --timeout=120s
  pod=$(kubectl --context "$context" -n fleet-system get pod -l "job-name=$run" -o jsonpath='{.items[0].metadata.name}')
  test "$(kubectl --context "$context" -n fleet-system get pod "$pod" -o jsonpath='{.spec.containers[0].image}')" = "$smoke_image"
  test "$(kubectl --context "$context" -n fleet-system get pod "$pod" -o jsonpath='{.spec.containers[0].imagePullPolicy}')" = "Never"
  test -n "$(kubectl --context "$context" -n fleet-system get pod "$pod" -o jsonpath='{.status.containerStatuses[0].imageID}')"
  kubectl --context "$context" -n fleet-system logs "job/$run"
}

run_phase allocate fleet-smoke-allocate-run
kubectl --context "$context" get machineclaims -A
test "$(kubectl --context "$context" -n fleet-project-a get machineclaim -o name)" = "machineclaim.fleet.datumapis.com/allocation"
test "$(kubectl --context "$context" -n fleet-project-b get machineclaim -o name)" = "machineclaim.fleet.datumapis.com/allocation"
test "$(kubectl --context "$context" -n fleet-project-a get machineclaim allocation -o jsonpath='{.status.phase}')" = "Ready"
test "$(kubectl --context "$context" -n fleet-project-b get machineclaim allocation -o jsonpath='{.status.phase}')" = "Pending"

run_phase install fleet-smoke-install-run
kubectl --context "$context" -n fleet-system rollout restart statefulset/fleet-simulator
kubectl --context "$context" -n fleet-system rollout status statefulset/fleet-simulator --timeout=120s
run_phase install fleet-smoke-install-recovery-run
kubectl --context "$context" -n fleet-project-a get installations,machineclaims
test "$(kubectl --context "$context" -n fleet-project-a get installation debian-12 -o jsonpath='{.status.phase}')" = "Ready"
test -n "$(kubectl --context "$context" -n fleet-project-a get installation debian-12 -o jsonpath='{.status.installationRevision}')"

for project in fleet-project-a fleet-project-b; do
  subject="system:serviceaccount:$project:default"
  for verb in get list watch create update patch delete; do
    if [ "$(kubectl --context "$context" auth can-i "$verb" secrets --as="$subject" -n fleet-system-private)" != "no" ]; then
      echo "$subject unexpectedly has $verb access to private records" >&2
      exit 1
    fi
  done
  if [ "$(kubectl --context "$context" auth can-i get secret/fleet-claim-forbidden --as="$subject" -n fleet-system-private)" != "no" ]; then
    echo "$subject unexpectedly has named private-record access" >&2
    exit 1
  fi
  if [ "$(kubectl --context "$context" auth can-i get secret/fleet-simulator-runtime --as="$subject" -n fleet-system)" != "no" ]; then
    echo "$subject unexpectedly has simulator credential access" >&2
    exit 1
  fi
done
smoke_subject=system:serviceaccount:fleet-system:fleet-standalone-smoke
test "$(kubectl --context "$context" auth can-i get machine/other-machine --as="$smoke_subject" -n fleet-provider)" = "no"
test "$(kubectl --context "$context" auth can-i update machineclass/standard-metal --as="$smoke_subject")" = "no"
test "$(kubectl --context "$context" auth can-i update machineprofile/general-purpose-24c-128g --as="$smoke_subject")" = "no"

run_phase release fleet-smoke-release-run
kubectl --context "$context" get machineclaims -A
if kubectl --context "$context" -n fleet-project-a get machineclaim allocation >/dev/null 2>&1; then
  echo "released project A claim still exists" >&2
  exit 1
fi
test "$(kubectl --context "$context" -n fleet-project-b get machineclaim -o name)" = "machineclaim.fleet.datumapis.com/allocation"
test "$(kubectl --context "$context" -n fleet-project-b get machineclaim allocation -o jsonpath='{.status.phase}')" = "Ready"
