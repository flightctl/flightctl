#!/usr/bin/env bash
set -ex

# Require root (run this script with sudo or as root)
if [[ "$EUID" -ne 0 ]]; then
  echo "This script must be run as root (use sudo)." >&2
  exit 1
fi

ROOT_OPTS=()

# Optional: --root <mock-root>
if [[ $# -gt 0 ]]; then
  if [[ "$1" == "--root" && -n "${2-}" ]]; then
    ROOT_OPTS=(--root "$2")
    shift 2
  else
    echo "Usage: $0 [--root MOCK_ROOT]" >&2
    exit 1
  fi
fi

# podman pull "${CI_RPM_IMAGE}"
# Reuse Go build/module caches from the host to speed up builds
# mkdir -p "${RPM_DNF_CACHE_DIR}" "${RPM_DNF_LIB_DIR}"
# Build the packit builder image
podman build -f hack/Containerfile.packit_builder -t packit-builder:latest

# Run the build in the container, mounting the current repo
podman run --rm \
  --privileged \
  --network=host \
  -v "$PWD:/work:z" \
  -w /work \
  packit-builder:latest \
  ./hack/build_rpms_packit.sh "${ROOT_OPTS[@]}"
