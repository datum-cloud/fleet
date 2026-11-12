#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
E2E_DIR="${ROOT_DIR}/.e2e"
OUT="${E2E_DIR}/diagnostics/$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "${OUT}"
for cluster in management location; do
  kubeconfig="${E2E_DIR}/${cluster}.kubeconfig"
  if [[ -f "${kubeconfig}" ]]; then
    kubectl --kubeconfig "${kubeconfig}" get all,events -A -o wide >"${OUT}/${cluster}-resources.txt" 2>&1 || true
    kubectl --kubeconfig "${kubeconfig}" get machinemanagementsessions,machineallocationbindings,machinemanagementsessionbindings,projectedmachinemanagementsessions -A -o yaml >"${OUT}/${cluster}-fleet.yaml" 2>&1 || true
    kubectl --kubeconfig "${kubeconfig}" logs -A --all-containers --prefix --tail=-1 >"${OUT}/${cluster}-logs.txt" 2>&1 || true
    if [[ "${cluster}" == location ]]; then
      kubectl --kubeconfig "${kubeconfig}" -n machine-access-system exec deployment/fleet-gateway -- \
        sh -c 'for f in /var/lib/fleet/operations/*.json; do test -f "$f" && { echo "$f"; cat "$f"; }; done' \
        >"${OUT}/operation-journal.jsonl" 2>&1 || true
    fi
  fi
done
if [[ -f "${E2E_DIR}/karmada.kubeconfig" ]]; then
  kubectl --kubeconfig "${E2E_DIR}/karmada.kubeconfig" get clusters,works.work.karmada.io,resourcebindings.work.karmada.io -A -o yaml >"${OUT}/karmada-resources.yaml" 2>&1 || true
  kubectl --kubeconfig "${E2E_DIR}/karmada.kubeconfig" get projectedmachinemanagementsessions,propagationpolicies -A -o yaml >"${OUT}/karmada-fleet.yaml" 2>&1 || true
fi
docker logs fleet-e2e-virtual-bmc >"${OUT}/virtual-bmc.log" 2>&1 || true
docker exec fleet-e2e-virtual-bmc virsh list --all >"${OUT}/libvirt-domains.txt" 2>&1 || true
docker exec fleet-e2e-virtual-bmc virsh dumpxml allocated >"${OUT}/allocated-domain.xml" 2>&1 || true
if [[ -d "${E2E_DIR}/virtual-bmc/evidence" ]]; then cp -R "${E2E_DIR}/virtual-bmc/evidence" "${OUT}/"; fi
echo "diagnostics: ${OUT}"
