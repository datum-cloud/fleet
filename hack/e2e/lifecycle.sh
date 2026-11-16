#!/usr/bin/env bash

fixture() {
  "${E2E_DIR}/bin/fleet-session-fixture" --synthetic --kubeconfig "${MGMT_KUBECONFIG}" \
    --state "${E2E_DIR}/session-executor.json" "$@"
}

allocate_fixture() {
  fixture --phase=allocate --machine "$1" --capacity "$2" \
    --principal system:serviceaccount:default:fleet-authorized --member-cluster fleet-location-e2e
}

binding_clean() {
  local binding=$1 object
  object=$(kubectl --request-timeout=10s --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system \
    get machinemanagementsessionbinding "${binding}" -o json) || return 1
  jq -e '.metadata.generation as $generation | any(.status.conditions[]?;
    .type == "CleanupConfirmed" and .status == "True" and .observedGeneration == $generation)' <<<"${object}" >/dev/null
}

asset_state() {
  local uid=$1 state=$2 objects
  objects=$(kubectl --request-timeout=10s --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system \
    get secrets -l fleet.datumapis.com/private-record=asset -o json) || return 1
  jq -e --arg uid "${uid}" --arg state "${state}" '
    [.items[].data["record.json"] | @base64d | fromjson | select(.claimUID == $uid)] |
    length == 1 and all(.[]; .state == $state)' <<<"${objects}" >/dev/null
}

release_blocked() {
  local machine=$1 uid=$2
  if fixture --phase=release --machine "${machine}" --machine-uid "${uid}" \
    >"${E2E_DIR}/release-${machine}.json" 2>"${E2E_DIR}/release-${machine}.log"; then
    fail "release of ${machine} bypassed active management custody"
  fi
  grep -q 'management session teardown is not verified' "${E2E_DIR}/release-${machine}.log" || fail "unexpected release failure for ${machine}"
  jq -e '.State == "Releasing" and .SafeToRemoveFinalizer == false' "${E2E_DIR}/release-${machine}.json" >/dev/null || fail "release did not report blocked custody"
  asset_state "${uid}" Releasing || fail "release did not durably mark asset Releasing"
}

release_fixture() {
  local machine=$1 uid=$2 bindings binding
  if fixture --phase=release --machine "${machine}" --machine-uid "${uid}" \
    >"${E2E_DIR}/release-${machine}.json" 2>"${E2E_DIR}/release-${machine}.log"; then
    jq -e '.SafeToRemoveFinalizer == true' "${E2E_DIR}/release-${machine}.json" >/dev/null
    return
  fi
  grep -q 'management session teardown is not verified' "${E2E_DIR}/release-${machine}.log" || fail "unexpected release failure for ${machine}"
  bindings=$(kubectl --request-timeout=10s --kubeconfig "${MGMT_KUBECONFIG}" -n fleet-system get machinemanagementsessionbindings -o json |
    jq -r --arg uid "${uid}" '.items[] | select(.spec.machineUID == $uid) | .metadata.name') || return 1
  for binding in ${bindings}; do
    wait_for "verified teardown for ${binding}; unresolved custody requires whole disposable environment teardown" "binding_clean '${binding}'"
  done
  fixture --phase=release --machine "${machine}" --machine-uid "${uid}" >"${E2E_DIR}/release-${machine}.json"
  jq -e '.SafeToRemoveFinalizer == true' "${E2E_DIR}/release-${machine}.json" >/dev/null
}
