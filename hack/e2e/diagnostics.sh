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
    kubectl --kubeconfig "${kubeconfig}" get machines,machinecapacities,machineallocations,machinemanagementsessions,machinemanagementsessionbindings,projectedmachinemanagementsessions -A -o yaml >"${OUT}/${cluster}-fleet.yaml" 2>&1 || true
    kubectl --kubeconfig "${kubeconfig}" logs --all-namespaces --all-containers --prefix --tail=-1 >"${OUT}/${cluster}-logs.txt" 2>&1 || true
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
if [[ -d "${E2E_DIR}/virtual-bmc/evidence" ]]; then
  # libvirt creates console logs as root. Make only non-secret evidence
  # readable, and never copy the optional OpenVPN key into CI diagnostics.
  docker exec fleet-e2e-virtual-bmc sh -c \
    'find /evidence -maxdepth 1 -type f ! -name "*.key" -exec chmod 0644 {} +' >/dev/null 2>&1 || true
  mkdir -p "${OUT}/evidence"
  find "${E2E_DIR}/virtual-bmc/evidence" -maxdepth 1 -type f ! -name '*.key' \
    -exec cp {} "${OUT}/evidence/" \;
fi
echo "diagnostics: ${OUT}"
