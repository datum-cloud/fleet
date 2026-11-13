#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
SCHEMA_DIR="${ROOT_DIR}/.e2e/redfish-schemas"
REVISION=4f81814e399213c9055863ddaff42791c6b3706a
SHA256=48d1c587676f2b03d5589825653d8924d0fee67d963c5de5ba411e9a3b4b4068
ARCHIVE="${ROOT_DIR}/.e2e/redfish-publications-${REVISION}.tar.gz"
mkdir -p "${SCHEMA_DIR}"
if [[ ! -f "${ARCHIVE}" ]]; then
  curl --fail --silent --show-error --location --retry 3 --max-time 180 \
    "https://codeload.github.com/DMTF/Redfish-Publications/tar.gz/${REVISION}" -o "${ARCHIVE}.download"
  mv "${ARCHIVE}.download" "${ARCHIVE}"
fi
actual=$(openssl dgst -sha256 "${ARCHIVE}" | awk '{print $NF}')
[[ "${actual}" == "${SHA256}" ]] || { echo "DMTF schema archive checksum mismatch: ${ARCHIVE}" >&2; exit 1; }
rm -rf "${SCHEMA_DIR}"
mkdir -p "${SCHEMA_DIR}"
tar -xzf "${ARCHIVE}" --strip-components=2 -C "${SCHEMA_DIR}" "Redfish-Publications-${REVISION}/csdl"
printf 'DMTF Redfish-Publications 2026.2\nrevision=%s\narchive_sha256=%s\n' "${REVISION}" "${SHA256}" >"${SCHEMA_DIR}/provenance.txt"
echo "Pinned DMTF CSDL schemas: ${SCHEMA_DIR}"
