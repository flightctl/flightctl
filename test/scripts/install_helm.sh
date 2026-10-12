#!/usr/bin/env bash
set -x -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
HELM_BIN="${ROOT_DIR}/bin/helm"

if command -v helm >/dev/null 2>&1 && helm version --short >/dev/null 2>&1; then
    echo "Helm already installed: $(helm version --short)"
    exit 0
fi

# Keep development tools in the checkout so installing them never needs host
# privilege and never replaces a system-wide Helm binary.
mkdir -p "${ROOT_DIR}/bin"
helm_script="$(mktemp "${TMPDIR:-/tmp}/get_helm.XXXXXX")"
trap 'rm -f "${helm_script}"' EXIT

curl -fsSL -o "${helm_script}" https://raw.githubusercontent.com/helm/helm/0d0f91d1ce277b2c8766cdc4c7aa04dbafbf2503/scripts/get-helm-3
echo "6701e269a95eec0a5f67067f504f43ad94e9b4a52ec1205d26b3973d6f5cb3dc  ${helm_script}" | sha256sum --check
chmod a+x "${helm_script}"
HELM_INSTALL_DIR="${ROOT_DIR}/bin" "${helm_script}"
test -x "${HELM_BIN}"
