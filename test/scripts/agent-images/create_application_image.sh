#!/usr/bin/env bash
set -ex
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "${SCRIPT_DIR}"/../functions

REGISTRY_ADDRESS=$(registry_address)
APP_REPO="${APP_REPO:-quay.io/flightctl}"
SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$(git describe --tags --exclude latest 2>/dev/null || echo "v0.0.0-unknown")}"
TAG="${TAG:-$SOURCE_GIT_TAG}"

cd "$ROOT_DIR"

# Build app images using the modular build.sh script
echo -e "\033[32mBuilding app images using build.sh --apps\033[m"
sudo "${SCRIPT_DIR}/scripts/build.sh" --apps

# Push the built app images to the registry
echo -e "\033[32mPushing app images to registry\033[m"

# Query built app images using podman filters (similar to bundle.sh)
mapfile -t refs < <(
  podman images --format '{{.Repository}}:{{.Tag}}' \
    --filter "label=io.flightctl.e2e.component=app" \
    --filter "reference=${APP_REPO}/*" || true
)

if [ "${#refs[@]}" -eq 0 ]; then
  echo "No app images found with label io.flightctl.e2e.component=app in ${APP_REPO}" >&2
  exit 1
fi

echo -e "\033[32mFound ${#refs[@]} app images to push:\033[m"
for ref in "${refs[@]}"; do
  printf '\t- %s\n' "${ref}"
done

# Push each found image to the registry
for ref in "${refs[@]}"; do
   # Extract app name and tag from the reference
   # Format: quay.io/flightctl/app-name:version or quay.io/flightctl/app-name:version-tag
   repo_tag="${ref#${APP_REPO}/}"  # Remove APP_REPO prefix
   app_name="${repo_tag%:*}"       # Everything before ':'
   tag="${repo_tag##*:}"           # Everything after ':'

   # Target images in registry
   registry_ref="${REGISTRY_ADDRESS}/${app_name}:${tag}"

   echo -e "\033[32mPushing ${ref} -> ${registry_ref}\033[m"

   # Tag for registry and push
   podman tag "${ref}" "${registry_ref}"
   podman push "${registry_ref}"

   echo -e "\033[32mSuccessfully pushed ${registry_ref}\033[m"
done

