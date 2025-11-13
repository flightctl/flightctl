#!/usr/bin/env bash
set -ex

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

# Reuse Go build/module caches from the host to speed up builds
HOST_GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}"
HOST_GOCACHE="${GOCACHE:-$HOME/.cache/go-build}"
mkdir -p "${HOST_GOMODCACHE}" "${HOST_GOCACHE}"

# Get the repository root directory (parent of hack/)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Reuse DNF caches stored in the repo (persisted between runs and in CI)
RPM_DNF_CACHE_DIR="${REPO_ROOT}/bin/rpm-dnf-cache"
RPM_DNF_LIB_DIR="${REPO_ROOT}/bin/rpm-dnf-lib"
mkdir -p "${RPM_DNF_CACHE_DIR}" "${RPM_DNF_LIB_DIR}"

# Populate version environment variables if not already set (same logic as Makefile)
# Change to repo root for consistent path resolution
cd "${REPO_ROOT}"

if [[ -z "${SOURCE_GIT_TAG:-}" ]]; then
  SOURCE_GIT_TAG="$(./hack/current-version)"
fi

if [[ -z "${SOURCE_GIT_TREE_STATE:-}" ]]; then
  if [[ ! -d ".git/" ]] || git diff --quiet; then
    SOURCE_GIT_TREE_STATE="clean"
  else
    SOURCE_GIT_TREE_STATE="dirty"
  fi
fi

if [[ -z "${SOURCE_GIT_COMMIT:-}" ]]; then
  SOURCE_GIT_COMMIT="$(git rev-parse --short "HEAD^{commit}" 2>/dev/null || echo "unknown")"
fi

if [[ -z "${BIN_TIMESTAMP:-}" ]]; then
  BIN_TIMESTAMP="$(date +'%Y%m%d')"
fi

if [[ -z "${SOURCE_GIT_TAG_NO_V:-}" ]]; then
  SOURCE_GIT_TAG_NO_V="$(echo "${SOURCE_GIT_TAG}" | sed 's/^v//')"
fi

# Debug: Print computed version variables
echo "Version variables:"
echo "  SOURCE_GIT_TAG=${SOURCE_GIT_TAG}"
echo "  SOURCE_GIT_TAG_NO_V=${SOURCE_GIT_TAG_NO_V}"
echo "  SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE}"
echo "  SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT}"
echo "  BIN_TIMESTAMP=${BIN_TIMESTAMP}"

CONTAINER_GOPATH="/root/go"
CONTAINER_GOMODCACHE="${CONTAINER_GOPATH}/pkg/mod"
CONTAINER_GOCACHE="/root/.cache/go-build"

PACKIT_BUILDER_IMAGE="${PACKIT_BUILDER_IMAGE:-quay.io/flightctl-tests/packit-builder:latest}"

if [[ "${REBUILD_IMAGE}" == "true" ]]; then
  podman build -f hack/Containerfile.packit_builder -t "${PACKIT_BUILDER_IMAGE}"
else
  podman pull "${PACKIT_BUILDER_IMAGE}"
fi

  # -v "${RPM_DNF_CACHE_DIR}:/var/cache/dnf:Z" \
  # -v "${RPM_DNF_LIB_DIR}:/var/lib/dnf:Z" \
# Run the build in the container, mounting the repo root
podman run --rm \
  --privileged \
  --network=host \
  -v "${REPO_ROOT}:/work:z" \
  -v "${REPO_ROOT}/hack/mock-site-defaults.cfg:/etc/mock/site-defaults.cfg:Z" \
  -v "${HOST_GOMODCACHE}:${CONTAINER_GOMODCACHE}" \
  -v "${HOST_GOCACHE}:${CONTAINER_GOCACHE}" \
  -e GOPATH="${CONTAINER_GOPATH}" \
  -e GOMODCACHE="${CONTAINER_GOMODCACHE}" \
  -e GOCACHE="${CONTAINER_GOCACHE}" \
  -v "${RPM_DNF_CACHE_DIR}:/var/cache/dnf:Z" \
  -v "${RPM_DNF_LIB_DIR}:/var/lib/dnf:Z" \
  -e SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
  -e SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE}" \
  -e SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT}" \
  -e SOURCE_GIT_TAG_NO_V="${SOURCE_GIT_TAG_NO_V}" \
  -e BIN_TIMESTAMP="${BIN_TIMESTAMP}" \
  -w /work \
  "${PACKIT_BUILDER_IMAGE}" \
  ./hack/build_rpms_packit.sh ${ROOT_OPTS[@]+"${ROOT_OPTS[@]}"}
