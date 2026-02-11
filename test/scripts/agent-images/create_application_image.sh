#!/usr/bin/env bash
set -ex
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "${SCRIPT_DIR}"/../functions

# Use same defaults as create_agent_images.sh
SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$(${ROOT_DIR}/hack/current-version)}"
TAG="${TAG:-$SOURCE_GIT_TAG}"
APP_REPO="${APP_REPO:-quay.io/flightctl}"
REGISTRY_ADDRESS="${REGISTRY_ADDRESS:-$(registry_address)}"

# Export variables for downstream scripts
export APP_REPO
export TAG
export REGISTRY_ADDRESS

cd "$ROOT_DIR"

# Build app images using the modular build.sh script
echo -e "\033[32mBuilding app images using build.sh --apps\033[m"
"${SCRIPT_DIR}/scripts/build.sh" --apps

# Create bundle tar using the modular bundle.sh script
echo -e "\033[32mBundling app images\033[m"
BUNDLE_TAR="${ROOT_DIR}/bin/app-images-bundle.tar"
"${SCRIPT_DIR}/scripts/bundle.sh" --filter "label=io.flightctl.e2e.component=app" --filter "reference=${APP_REPO}/sleep-app:*" --output-path "${BUNDLE_TAR}"

# Push images if requested using the modular upload-images.sh script
if [ "${PUSH_IMAGES:-false}" = "true" ]; then
  echo -e "\033[32mPushing app images to registry\033[m"
  "${SCRIPT_DIR}/scripts/upload-images.sh" "${BUNDLE_TAR}"
else
  echo -e "\033[33mSkipping push (PUSH_IMAGES=${PUSH_IMAGES:-false})\033[m"
fi

