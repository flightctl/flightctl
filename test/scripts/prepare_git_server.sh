#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

# Build git-server container with proper caching
# Use GitHub Actions cache when GITHUB_ACTIONS=true, otherwise no caching
if [ "${GITHUB_ACTIONS:-false}" = "true" ]; then
    REGISTRY="${REGISTRY:-localhost}"
    REGISTRY_OWNER="${REGISTRY_OWNER:-flightctl}"
    CACHE_FLAGS=("--cache-from=${REGISTRY}/${REGISTRY_OWNER}/git-server")
else
    CACHE_FLAGS=()
fi

podman build "${CACHE_FLAGS[@]}" \
	-f test/scripts/Containerfile.gitserver -t localhost/git-server:latest .

# can be tested with: 
# podman run -d --restart always -p 1213:22 --name gitserver --cap-add sys_chroot localhost/git-server:latest
# podman rm gitserver --force
