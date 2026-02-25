#!/usr/bin/env bash
set -euo pipefail

# Default to el9 if FLAVOR not specified
FLAVOR=${FLAVOR:-el9}

# Validate FLAVOR parameter
case "$FLAVOR" in
    el9|el10) ;; # Valid values
    *)
        echo "Error: Invalid FLAVOR '$FLAVOR'. Must be 'el9' or 'el10'"
        exit 1
        ;;
esac

# Get script directory and load configuration functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/container-config.sh
source "${SCRIPT_DIR}/container-config.sh"

ROOT_DIR="$(dirname "$SCRIPT_DIR")"
cd "$ROOT_DIR"

# Validate and load flavor configuration
if ! validate_flavor "$FLAVOR"; then
    exit 1
fi

if ! load_flavor_config "$FLAVOR"; then
    exit 1
fi

# Use MINIMAL_IMAGE from config as BASE_IMAGE
BASE_IMAGE="$MINIMAL_IMAGE"
# Extract tag from the image for naming
BASE_TAG="${MINIMAL_IMAGE##*:}"

echo "Building base image for $FLAVOR using Containerfile.base"
echo "Base image: $BASE_IMAGE"

IMAGE_REPO=${IMAGE_REPO:-quay.io/flightctl/flightctl-base}

# Get architecture for tagging
arch=$(uname -m)
case $arch in
    x86_64) arch=amd64;;
    aarch64) arch=arm64;;
esac

# Create tags (matching the old script naming)
ARCH_TAG="$FLAVOR-$arch-$BASE_TAG"
COMMON_TAG="$FLAVOR-$BASE_TAG"

echo "Building with podman using Containerfile.base..."
podman build \
    -f images/Containerfile.base \
    --build-arg BASE_IMAGE="$BASE_IMAGE" \
    --build-arg EL_VERSION="$EL_VERSION" \
    -t "$IMAGE_REPO:$ARCH_TAG" \
    -t "$IMAGE_REPO:$COMMON_TAG" \
    .

echo "✓ Built base image with tags:"
echo "  - $IMAGE_REPO:$ARCH_TAG (architecture-specific)"
echo "  - $IMAGE_REPO:$COMMON_TAG (common)"
echo ""
echo "To push to registry:"
echo "  podman push $IMAGE_REPO:$ARCH_TAG"
echo "  podman push $IMAGE_REPO:$COMMON_TAG"
