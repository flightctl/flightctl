#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KIND_CONFIG=${KIND_CONFIG:-test/scripts/kind_cluster.yaml}
"${SCRIPT_DIR}/install_kind_kvm.sh" --check-host

# Omit the device mount for an explicitly disabled worker or TCG development
# build when the host has no KVM device. Both local deploy and CI use this path.
if [[ ${IMAGEBUILDER_WORKER_ENABLED:-true} == true && ${IMAGEBUILDER_VM_ACCELERATOR:-kvm} == kvm && -c /dev/kvm ]]; then
    kind create cluster --config "$KIND_CONFIG"
else
    generated_config=$(mktemp)
    trap 'rm -f "$generated_config"' EXIT
    sed '/# BEGIN OPTIONAL KVM MOUNT/,/# END OPTIONAL KVM MOUNT/d' "$KIND_CONFIG" > "$generated_config"
    kind create cluster --config "$generated_config"
fi

"${SCRIPT_DIR}/install_kind_kvm.sh"
if [[ -n ${GATEWAY:-} ]]; then
    "${SCRIPT_DIR}/gateway/install-gateway.sh"
fi
