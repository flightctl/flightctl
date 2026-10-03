#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
KIND_VERSION=0.26.0
KIND_BIN="${ROOT_DIR}/bin/kind"

EXISTING_VERSION=""
if command -v kind >/dev/null 2>&1; then
    EXISTING_VERSION="$(kind version 2>&1 | awk '{print $2}' | sed 's/^v//' || true)"
fi
if [[ "${EXISTING_VERSION}" == "${KIND_VERSION}" ]]; then
    echo "Kind v${KIND_VERSION} already installed"
    exit 0
fi

echo "Installing kind v${KIND_VERSION} under ${ROOT_DIR}/bin"
mkdir -p "${ROOT_DIR}/bin"
GOBIN="${ROOT_DIR}/bin" go install "sigs.k8s.io/kind@v${KIND_VERSION}"
chmod +x "${KIND_BIN}"
