#!/usr/bin/env bash
set -ex

# This script runs the RPM build process using packit inside a Podman container.
# Inside the container, the RPM build is done in a mock chroot environment, making it closer to a "real" RPM build environment.



# Require root (run this script with sudo or as root)
if [[ "$EUID" -ne 0 ]]; then
  echo "This script must be run as root (use sudo)." >&2
  exit 1
fi

usage() {
  echo "Usage: $0 [--root MOCK_ROOT] [--rebuild-image]" >&2
  exit 1
}

ROOT_OPTS=()
REBUILD_IMAGE=false
PACKIT_BUILDER_IMAGE="${PACKIT_BUILDER_IMAGE:-quay.io/flightctl-tests/packit-builder:latest}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --root)
      if [[ -n "${2-}" ]]; then
        ROOT_OPTS=(--root "$2")
        shift 2
      else
        usage
      fi
      ;;
    --rebuild-image)
      REBUILD_IMAGE=true
      shift
      ;;
    *)
      usage
      ;;
  esac
done

# Get the repository root directory (parent of hack/)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Reuse Go build/module caches from the host to speed up builds
HOST_GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}"
HOST_GOCACHE="${GOCACHE:-$HOME/.cache/go-build}"
mkdir -p "${HOST_GOMODCACHE}" "${HOST_GOCACHE}"

CONTAINER_GOPATH="/root/go"
CONTAINER_GOMODCACHE="${CONTAINER_GOPATH}/pkg/mod"
CONTAINER_GOCACHE="/root/.cache/go-build"
CONTAINER_MOCKCACHE="/var/cache/mock"

cd "${REPO_ROOT}"

if [[ "${REBUILD_IMAGE}" == "true" ]]; then
  BASE_IMAGE="${PACKIT_BUILDER_IMAGE}-base"
  # 1. Build base image
  podman build --network=host --no-cache \
    -f hack/Containerfile.packit_builder \
    -t "${BASE_IMAGE}"

  # 2. Create a container that will run the prewarm script as PID 1
  CID="$(podman create \
    --privileged \
    --network=host \
    "${BASE_IMAGE}" \
    /usr/bin/mock_prewarm_caches.sh)"

  # 3. Run the script and wait for it to finish
  podman start -a "${CID}"

  # 4. Commit the resulting filesystem as the final image
  podman commit "${CID}" "${PACKIT_BUILDER_IMAGE}"

  # 5. Cleanup the temp container
  podman rm "${CID}"
else
  podman pull "${PACKIT_BUILDER_IMAGE}"
fi

# Run the build in the container, mounting the repo root
podman run --rm \
  --privileged \
  --network=host \
  -v "${REPO_ROOT}:/work:z" \
  -v "${HOST_GOMODCACHE}:${CONTAINER_GOMODCACHE}" \
  -v "${HOST_GOCACHE}:${CONTAINER_GOCACHE}" \
  -e GOPATH="${CONTAINER_GOPATH}" \
  -e GOMODCACHE="${CONTAINER_GOMODCACHE}" \
  -e GOCACHE="${CONTAINER_GOCACHE}" \
  -w /work \
  "${PACKIT_BUILDER_IMAGE}" \
  ./hack/build_rpms_packit.sh ${ROOT_OPTS[@]+"${ROOT_OPTS[@]}"}

