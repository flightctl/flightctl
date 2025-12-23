#!/usr/bin/env bash
set -euo pipefail

HELP_TEXT="Build Flight Control RPMs using packit in a containerized mock environment

This script builds RPMs using packit inside a Podman container. Supports both
local builds and mock chroot builds for different distributions.

Usage:
  sudo ./hack/build_rpms.sh [--root MOCK_ROOT] [--rebuild-image] [--pull] [--help]

Options:
  --root MOCK_ROOT    Use specific mock chroot (enables in-mock build with logs)
  --rebuild-image     Rebuild the packit base image and cache images only
                      (with --root only that root's cache image is rebuilt)
                      Does not build RPMs - exits after rebuilding images
  --pull              Force pull images from registry even if they exist locally
  --help              Show this help message

Examples:
  sudo ./hack/build_rpms.sh                                               # Local build
  sudo ./hack/build_rpms.sh --root centos-stream+epel-next-9-x86_64       # CentOS Stream 9
  sudo ./hack/build_rpms.sh --root epel-10-x86_64                         # RHEL 10 / Fedora 43
  sudo ./hack/build_rpms.sh --pull                                        # Local build with forced image pull
  sudo ./hack/build_rpms.sh --rebuild-image                               # Rebuild base + all cache images
  sudo ./hack/build_rpms.sh --rebuild-image --root epel-10-x86_64         # Rebuild base + epel-10 cache
"

##############################################################################
# Defaults and configuration
##############################################################################

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Configuration file for mock roots
MOCK_ROOTS_CONFIG="${SCRIPT_DIR}/mock-roots.conf"

ROOT=""
REBUILD_IMAGE=false
PULL_IMAGES=false
ROOT_OPTS=()

# Array to track built images for summary
BUILT_IMAGES=()

# Base builder image name. You can override it from the environment.
# Example: PACKIT_BUILDER_IMAGE=quay.io/flightctl-tests/packit-builder
PACKIT_BUILDER_IMAGE="${PACKIT_BUILDER_IMAGE:-quay.io/flightctl-tests/packit-builder}"

##############################################################################
# Helpers
##############################################################################

current_tree_state() {
  (cd "${REPO_ROOT}" && ( ( [ ! -d ".git" ] || git diff --quiet ) && echo "clean" ) || echo "dirty")
}

current_commit() {
  (cd "${REPO_ROOT}" && git rev-parse --short "HEAD^{commit}" 2>/dev/null) || echo "unknown"
}

ensure_version_env() {
  SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$(${SCRIPT_DIR}/current-version)}"
  SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE:-$(current_tree_state)}"
  SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT:-$(current_commit)}"
  export SOURCE_GIT_TAG SOURCE_GIT_TREE_STATE SOURCE_GIT_COMMIT
}

usage() {
  echo "Usage: $0 [--root MOCK_ROOT] [--rebuild-image] [--pull] [--help]" >&2
  echo "Use --help for detailed information and examples." >&2
  exit 1
}

print_help_if_requested() {
  for arg in "$@"; do
    if [[ "$arg" == "--help" || "$arg" == "-h" ]]; then
      echo "$HELP_TEXT"
      exit 0
    fi
  done
}

require_root() {
  if [[ "$EUID" -ne 0 ]]; then
    echo "This script must be run as root (use sudo)." >&2
    exit 1
  fi
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --root)
        if [[ -n "${2-}" ]]; then
          ROOT="$2"
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
      --pull)
        PULL_IMAGES=true
        shift
        ;;
      --help|-h)
        # already handled above, but keep for completeness
        echo "$HELP_TEXT"
        exit 0
        ;;
      *)
        usage
        ;;
    esac
  done
}

# Sanitizes a mock root so it can be used as part of a container tag.
# Allowed characters in tags: [A-Za-z0-9_.-]. Everything else becomes "_".
sanitize_tag_fragment() {
  printf '%s' "$1" | tr -c 'A-Za-z0-9_.-' '_'
}

init_image_names() {
  local image="$1"
  # Remove any existing tag from the image name
  IMAGE_REPO="${image%:*}"
  BASE_IMAGE="${IMAGE_REPO}:base"
}

# Read mock roots from configuration file
read_mock_roots() {
  if [[ ! -f "$MOCK_ROOTS_CONFIG" ]]; then
    echo "Error: Mock roots config file not found: $MOCK_ROOTS_CONFIG" >&2
    exit 1
  fi

  while IFS='=' read -r root_name tag_name || [[ -n "$root_name" ]]; do
    [[ "$root_name" =~ ^[[:space:]]*# ]] && continue
    [[ -z "$root_name" ]] && continue
    echo "${root_name}"
  done < "$MOCK_ROOTS_CONFIG"
}

# Get tag for a given root name
get_tag_for_root() {
  local root="$1"
  while IFS='=' read -r root_name tag_name || [[ -n "$root_name" ]]; do
    [[ "$root_name" =~ ^[[:space:]]*# ]] && continue
    [[ -z "$root_name" ]] && continue

    if [[ "$root_name" == "$root" ]]; then
      echo "$tag_name"
      return
    fi
  done < "$MOCK_ROOTS_CONFIG"
}

# Check if a root is present in mock-roots.conf
is_known_root() {
  local root="$1"
  while IFS='=' read -r root_name tag_name || [[ -n "$root_name" ]]; do
    [[ "$root_name" =~ ^[[:space:]]*# ]] && continue
    [[ -z "$root_name" ]] && continue

    if [[ "$root_name" == "$root" ]]; then
      return 0
    fi
  done < "$MOCK_ROOTS_CONFIG"
  return 1
}

root_image_for() {
  local root="$1"
  local tag
  tag="$(get_tag_for_root "$root")"
  echo "${IMAGE_REPO}:${tag}"
}

##############################################################################
# Image build functions
##############################################################################

build_base_image() {
  echo "Building base packit builder image: ${BASE_IMAGE}"
  podman build \
    --network=host \
    --no-cache \
    -f hack/Containerfile.packit_builder \
    -t "${BASE_IMAGE}"

  # Track built image for summary
  BUILT_IMAGES+=("base:${BASE_IMAGE}")
  echo "Built base image: ${BASE_IMAGE}"
}

build_root_cache_image() {
  local root="$1"
  local root_image
  root_image="$(root_image_for "$root")"

  echo "Building cache image for mock root '${root}' as '${root_image}'"

  # Create a container from the base image and run prewarm for this root only
  local cid
  cid="$(podman create \
    --privileged \
    --network=host \
    "${BASE_IMAGE}" \
    /usr/bin/mock_prewarm_caches.sh "$root")"

  # Run prewarm
  podman start -a "${cid}"

  # Commit container filesystem as the cache image for this root
  podman commit "${cid}" "${root_image}" >/dev/null

  # Clean up temporary container
  podman rm "${cid}" >/dev/null

  # Track built image for summary
  BUILT_IMAGES+=("cache:${root}:${root_image}")
  echo "Finished cache image for '${root}': ${root_image}"
}

rebuild_images() {
  # Always rebuild base image when --rebuild-image is used
  build_base_image

  local roots_to_build=()

  if [[ -n "$ROOT" ]]; then
    if is_known_root "$ROOT"; then
      roots_to_build=("$ROOT")
    else
      echo "WARNING: Mock root '${ROOT}' is not present in mock-roots.conf" >&2
      echo "WARNING: Skipping cache image rebuild for unknown root" >&2
    fi
  else
    # Read roots from config file
    mapfile -t roots_to_build < <(read_mock_roots)
  fi

  for r in "${roots_to_build[@]}"; do
    build_root_cache_image "$r"
  done

  print_build_summary
}

print_build_summary() {
  if [[ ${#BUILT_IMAGES[@]} -eq 0 ]]; then
    return
  fi

  echo ""
  echo "=== BUILD SUMMARY ==="
  echo "Successfully built ${#BUILT_IMAGES[@]} image(s):"

  for image_info in "${BUILT_IMAGES[@]}"; do
    if [[ "$image_info" == base:* ]]; then
      image_name="${image_info#base:}"
      echo "\t- Base image: ${image_name}"
    elif [[ "$image_info" == cache:* ]]; then
      # Format: cache:root_name:image_name
      rest="${image_info#cache:}"
      root_name="${rest%%:*}"
      image_name="${rest#*:}"
      echo "\t- Mock cache for '${root_name}': ${image_name}"
    fi
  done
  echo ""
}

# Check and pull a single image if needed
# Usage: check_and_pull_image "image_name"
# Returns: 0 if pull was performed, 1 if no pull needed
check_and_pull_image() {
  local image_name="$1"
  local need_pull=false

  # Check if we need to pull
  if [[ "$PULL_IMAGES" == true ]]; then
    need_pull=true
    echo "Force pulling ${image_name} due to --pull option"
  elif ! podman image exists "${image_name}"; then
    need_pull=true
    echo "Image ${image_name} not found locally"
  else
    echo "Image ${image_name} found locally"
  fi

  # Pull if needed
  if [[ "$need_pull" == true ]]; then
    echo "Pulling ${image_name}"
    podman pull "${image_name}"
    return 0
  else
    return 1
  fi
}

pull_images_if_needed() {
  local pulls_performed=0

  # Check and pull cache image if using a known mock root, otherwise pull base image
  if [[ -n "$ROOT" ]] && is_known_root "$ROOT"; then
    local root_image
    root_image="$(root_image_for "$ROOT")"
    if check_and_pull_image "${root_image}"; then
      pulls_performed=$((pulls_performed + 1))
    fi
  else
    if check_and_pull_image "${BASE_IMAGE}"; then
      pulls_performed=$((pulls_performed + 1))
    fi
  fi

  # Summary message if no pulls were needed
  if [[ $pulls_performed -eq 0 ]]; then
    echo "All required images are available locally"
  fi
}

##############################################################################
# Main build runner
##############################################################################

run_build_in_container() {
  local run_image

  if [[ -n "$ROOT" ]]; then
    if is_known_root "$ROOT"; then
      run_image="$(root_image_for "$ROOT")"
    else
      echo "WARNING: Mock root '${ROOT}' is not present in mock-roots.conf" >&2
      echo "WARNING: Falling back to base image (no pre-warmed cache)" >&2
      run_image="${BASE_IMAGE}"
    fi
  else
    run_image="${BASE_IMAGE}"
  fi

  echo "Using builder image: ${run_image}"
  if [[ -n "$ROOT" ]]; then
    if is_known_root "$ROOT"; then
      echo "Mock root: ${ROOT}"
    else
      echo "Mock root: ${ROOT} (unknown, using base image)"
    fi
  else
    echo "Local packit build (no mock root)"
  fi

  # Reuse Go build/module caches from the host to speed up builds
  local host_gomodcache host_gocache
  host_gomodcache="${GOMODCACHE:-$HOME/go/pkg/mod}"
  host_gocache="${GOCACHE:-$HOME/.cache/go-build}"
  mkdir -p "${host_gomodcache}" "${host_gocache}"

  local container_gopath container_gomodcache container_gocache
  container_gopath="/root/go"
  container_gomodcache="${container_gopath}/pkg/mod"
  container_gocache="/root/.cache/go-build"

  # Get the repository root directory (parent of hack/)
  local repo_root="${REPO_ROOT}"
  cd "${repo_root}"

  podman run --rm \
    --privileged \
    --network=host \
    -v "${repo_root}:/work:z" \
    -v "${host_gomodcache}:${container_gomodcache}" \
    -v "${host_gocache}:${container_gocache}" \
    -e GOPATH="${container_gopath}" \
    -e GOMODCACHE="${container_gomodcache}" \
    -e GOCACHE="${container_gocache}" \
    -e GITHUB_ACTIONS \
    -e SOURCE_GIT_TAG \
    -e SOURCE_GIT_TREE_STATE \
    -e SOURCE_GIT_COMMIT \
    -w /work \
    "${run_image}" \
    ./hack/build_rpms_packit.sh ${ROOT_OPTS[@]+"${ROOT_OPTS[@]}"}
}

##############################################################################
# Entry point
##############################################################################

print_help_if_requested "$@"
require_root
parse_args "$@"
ensure_version_env
init_image_names "${PACKIT_BUILDER_IMAGE}"

if [[ "${REBUILD_IMAGE}" == true ]]; then
  rebuild_images
  echo "Image rebuild completed."
  exit 0
else
  pull_images_if_needed
  run_build_in_container
fi
