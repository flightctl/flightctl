#!/usr/bin/env bash
set -ex
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

source "${SCRIPT_DIR}"/../functions

REGISTRY_ADDRESS=$(registry_address)
IMAGE_LIST=$(ls $SCRIPT_DIR | grep Containerfile-sleep-app | cut -d '-' -f 4)


for img in $IMAGE_LIST; do
   FINAL_REF="${REGISTRY_ADDRESS}/sleep-app:${img}"
   echo -e "\033[32mCreating image ${FINAL_REF} \033[m"
   # Use GitHub Actions cache when GITHUB_ACTIONS=true, otherwise no caching
   if [ "${GITHUB_ACTIONS:-false}" = "true" ]; then
       REGISTRY="${REGISTRY:-localhost}"
       REGISTRY_OWNER="${REGISTRY_OWNER:-flightctl}"
       CACHE_FLAGS=("--cache-from=${REGISTRY}/${REGISTRY_OWNER}/sleep-app")
   else
       CACHE_FLAGS=()
   fi

   podman build "${CACHE_FLAGS[@]}" \
   	--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG:-$(git describe --tags --exclude latest 2>/dev/null || echo "latest")} \
   	--build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE:-$( ( ( [ ! -d ".git/" ] || git diff --quiet ) && echo 'clean' ) || echo 'dirty' )} \
   	--build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT:-$(git rev-parse --short "HEAD^{commit}" 2>/dev/null || echo "unknown")} \
   	-f "${SCRIPT_DIR}"/Containerfile-sleep-app-"${img}" -t localhost:5000/sleep-app:${img} .
   podman tag "localhost:5000/sleep-app:${img}" "${FINAL_REF}"
   podman push "${FINAL_REF}"
done

