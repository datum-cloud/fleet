#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
E2E_DIR="${ROOT_DIR}/.e2e"
OUT="${E2E_DIR}/diagnostics/$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "${OUT}"
source "${ROOT_DIR}/hack/e2e/common.sh"
for cluster in management location; do
  kubeconfig="${E2E_DIR}/${cluster}.kubeconfig"
  if [[ -f "${kubeconfig}" ]]; then
    kubectl --request-timeout=5s --kubeconfig "${kubeconfig}" get all,events -A -o wide >"${OUT}/${cluster}-resources.txt" 2>&1 || true
    kubectl --request-timeout=5s --kubeconfig "${kubeconfig}" get pods -A -o json >"${OUT}/${cluster}-pods.json" 2>&1 || true
    for resource in machines machinecapacities machineallocations machinemanagementsessions machinemanagementsessionbindings projectedmachinemanagementsessions; do
      kubectl --request-timeout=5s --kubeconfig "${kubeconfig}" get "${resource}" -A -o yaml >"${OUT}/${cluster}-${resource}.yaml" 2>&1 || true
    done
    if [[ "${cluster}" == management ]]; then
      workloads='fleet-system/fleet-controller karmada-system/karmada-controller-manager karmada-system/karmada-apiserver karmada-system/karmada-kube-controller-manager'
    else
      workloads='machine-access-system/fleet-gateway machine-access-system/fleet-relay'
    fi
    for workload in ${workloads}; do
      kubectl --request-timeout=5s --kubeconfig "${kubeconfig}" -n "${workload%/*}" logs "deployment/${workload#*/}" \
        --all-containers --prefix --timestamps --tail=2000 >"${OUT}/${cluster}-${workload#*/}.log" 2>&1 || true
      kubectl --request-timeout=5s --kubeconfig "${kubeconfig}" -n "${workload%/*}" logs "deployment/${workload#*/}" \
        --all-containers --previous --prefix --timestamps --tail=500 >"${OUT}/${cluster}-${workload#*/}-previous.log" 2>&1 || true
    done
    if [[ "${cluster}" == location ]]; then
      run_timeout 15 kubectl --request-timeout=5s --kubeconfig "${kubeconfig}" -n machine-access-system exec deployment/fleet-gateway -- \
        sh -c 'for f in /var/lib/fleet/operations/*.json; do test -f "$f" && { echo "$f"; cat "$f"; }; done' \
        >"${OUT}/operation-journal.jsonl" 2>&1 || true
    fi
  fi
done
if [[ -f "${E2E_DIR}/karmada.kubeconfig" ]]; then
  kubectl --request-timeout=5s --kubeconfig "${E2E_DIR}/karmada.kubeconfig" get clusters,works.work.karmada.io,resourcebindings.work.karmada.io -A -o yaml >"${OUT}/karmada-resources.yaml" 2>&1 || true
  kubectl --request-timeout=5s --kubeconfig "${E2E_DIR}/karmada.kubeconfig" get projectedmachinemanagementsessions,propagationpolicies -A -o yaml >"${OUT}/karmada-fleet.yaml" 2>&1 || true
fi
docker logs fleet-e2e-virtual-bmc >"${OUT}/virtual-bmc.log" 2>&1 || true
docker exec fleet-e2e-virtual-bmc virsh list --all >"${OUT}/libvirt-domains.txt" 2>&1 || true
docker exec fleet-e2e-virtual-bmc virsh dumpxml allocated >"${OUT}/allocated-domain.xml" 2>&1 || true
if [[ -d "${E2E_DIR}/virtual-bmc/evidence" ]]; then
  mkdir -p "${OUT}/evidence"
  for evidence in acceleration.txt allocated-console.log pxe-callbacks.jsonl libvirtd.log virtlogd.log; do
    docker cp "fleet-e2e-virtual-bmc:/evidence/${evidence}" "${OUT}/evidence/${evidence}" 2>/dev/null || true
  done
  cp "${E2E_DIR}/virtual-bmc/evidence/pxe-operation-correlation.json" "${OUT}/evidence/" 2>/dev/null || true
fi
echo "diagnostics: ${OUT}"
