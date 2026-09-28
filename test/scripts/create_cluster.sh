#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOTLESS_CONFIG="${SCRIPT_DIR}/kind_cluster_rootless.yaml"
ROOTFUL_CONFIG="${SCRIPT_DIR}/kind_cluster.yaml"

if [[ "$(id -u)" -eq 0 ]]; then
    kind create cluster --config "${ROOTFUL_CONFIG}"
else
    export KIND_EXPERIMENTAL_PROVIDER=podman
    kind_podman="$(command -v podman)"
    podman_wrapper_dir="$(mktemp -d "${TMPDIR:-/tmp}/flightctl-kind-podman.XXXXXX")"
    trap 'rm -rf -- "${podman_wrapper_dir}"' EXIT
    ln -s "${SCRIPT_DIR}/podman_kind_wrapper.sh" "${podman_wrapper_dir}/podman"
    export FLIGHTCTL_KIND_REAL_PODMAN="${kind_podman}"
    export PATH="${podman_wrapper_dir}:${PATH}"
    systemd-run --scope --user -p Delegate=yes \
        kind create cluster --config "${ROOTLESS_CONFIG}"
fi

if [[ "${GATEWAY:-}" ]]; then
    test/scripts/gateway/install-gateway.sh
fi

echo ""
