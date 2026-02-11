#!/usr/bin/env bash
set -euo pipefail

# Orchestrates per-flavor build: variants+bundle and qcow2 in parallel.
# Requirements:
#   - Environment:
#       TAG            (image tag)
#       IMAGE_REPO     (quay.io/flightctl/flightctl-device by default in callers)
#       OS_ID          (flavor id, e.g., cs9-bootc or cs10-bootc)
#   - Tools:
#       ./build.sh, ./bundle.sh, ./qcow2.sh, ./upload-images.sh in the same directory
#
# Usage:
#   OS_ID=cs9-bootc TAG=vX IMAGE_REPO=... ./build_and_qcow2.sh
#   ./build_and_qcow2.sh --os-id cs10-bootc --push
#

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

# Separate paths for different outputs
ARTIFACTS_OUTPUT_DIR="${ARTIFACTS_OUTPUT_DIR:-${ROOT_DIR}/bin/agent-artifacts}"

OS_ID_ENV="${OS_ID:-}"
SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$(${ROOT_DIR}/hack/current-version)}"
TAG="${TAG:-$SOURCE_GIT_TAG}"
IMAGE_REPO="${IMAGE_REPO:-quay.io/flightctl/flightctl-device}"
DO_PUSH=false
SKIP_QCOW_BUILD="${SKIP_QCOW_BUILD:-false}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --os-id)
      OS_ID_ENV="$2"
      shift 2
      ;;
    --push)
      DO_PUSH=true
      shift
      ;;
    *)
      echo "Unknown option: $1" >&2
      echo "Usage: $0 [--os-id OS_ID] [--push]" >&2
      exit 1
      ;;
  esac
done

if [ -z "${OS_ID_ENV}" ]; then
  echo "OS_ID must be provided via env or --os-id" >&2
  exit 1
fi

export OS_ID="${OS_ID_ENV}"
export AGENT_OS_ID="${OS_ID}"

# Set QCOW2 output directory now that OS_ID is available
QCOW2_OUTPUT_DIR="${QCOW2_OUTPUT_DIR:-${ROOT_DIR}/bin/output/agent-qcow2-${OS_ID}}"

LOG_DIR="${ARTIFACTS_OUTPUT_DIR}/logs-${OS_ID}"
mkdir -p "${LOG_DIR}"
variants_log="${LOG_DIR}/variants.log"
qcow2_log="${LOG_DIR}/qcow2.log"

echo "Building variants and creating bundle for ${OS_ID}"
echo "Variants log: ${variants_log}"
echo "QCOW2 log: ${qcow2_log}"

sudo rm -f "${variants_log}" "${qcow2_log}"

(
  set -euo pipefail
  echo "Building variants and creating bundle for ${OS_ID}"
  sudo -E "${SCRIPT_DIR}/build.sh" --variants 2>&1 | tee "${variants_log}"

  printf '%s\n' "----------" "Bundle variants" "----------"

  sudo -E "${SCRIPT_DIR}/bundle.sh" \
    --filter "label=io.flightctl.e2e.component" \
    --filter "reference=${IMAGE_REPO}:*-${OS_ID}-*" \
    --output-path "${ARTIFACTS_OUTPUT_DIR}/agent-images-bundle-${OS_ID}.tar" 2>&1 | tee -a "${variants_log}"
  sudo chown -R "$(id -un)":"$(id -gn)" "${ARTIFACTS_OUTPUT_DIR}" || true

  # Push images if requested
  if [ "${DO_PUSH}" = "true" ]; then
    BUNDLE_TAR="${ARTIFACTS_OUTPUT_DIR}/agent-images-bundle-${OS_ID}.tar"
    if [ -f "${BUNDLE_TAR}" ]; then
      echo "Pushing images from bundle..."
      "${SCRIPT_DIR}/upload-images.sh" "${BUNDLE_TAR}" 2>&1 | tee -a "${variants_log}"
    else
      echo "Warning: Bundle not found at ${BUNDLE_TAR}, skipping push"
    fi
  fi
) &
VARIANTS_PID=$!

QCOW2_PID=""
if [ "${SKIP_QCOW_BUILD}" != "true" ]; then
  (
    set -euo pipefail
    echo "Building qcow2 for ${OS_ID}"
    OUTPUT_DIR="${QCOW2_OUTPUT_DIR}" "${SCRIPT_DIR}/qcow2.sh" 2>&1 | tee "${qcow2_log}"
    sudo chown -R "$(id -un)":"$(id -gn)" "${QCOW2_OUTPUT_DIR}" || true
    echo "endgroup"
  ) &
  QCOW2_PID=$!
else
  echo "Skipping qcow2 build for ${OS_ID} (SKIP_QCOW_BUILD=true)"
fi

variants_exit=0
qcow2_exit=0
wait "${VARIANTS_PID}" || variants_exit=$?
if [ -n "${QCOW2_PID}" ]; then
  wait "${QCOW2_PID}" || qcow2_exit=$?
fi

if [ "${variants_exit}" -ne 0 ]; then
  echo "::error::Variants+bundle build failed with exit code ${variants_exit}"
  echo "The logs for the variants build are saved to ${variants_log}"
  exit "${variants_exit}"
fi
if [ -n "${QCOW2_PID}" ] && [ "${qcow2_exit}" -ne 0 ]; then
  echo "::error::QCOW2 build failed with exit code ${qcow2_exit}"
  echo "The logs for the qcow2 build are saved to ${qcow2_log}"
  exit "${qcow2_exit}"
fi

echo "Build and qcow2 for ${OS_ID} completed successfully."
