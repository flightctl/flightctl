#!/usr/bin/env bash
set -euo pipefail

# Default to el9 if OS_ID not specified
OS_ID=${OS_ID:-el9}

# Convert cs9/cs10 to el9/el10 for compatibility
case "$OS_ID" in
    cs9) OS_ID="el9" ;;
    cs10) OS_ID="el10" ;;
    el9|el10) ;; # Already correct format
    *)
        echo "Error: Invalid OS_ID '$OS_ID'. Must be 'el9', 'el10', 'cs9', or 'cs10'"
        exit 1
        ;;
esac

# Get script directory and change to repo root
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
cd "$ROOT_DIR"

# Set base image and version info based on OS_ID (matching publish_containers.sh)
case "$OS_ID" in
    el9)
        BASE_IMAGE="registry.access.redhat.com/ubi9/ubi-minimal:9.7-1763362218"
        EL_VERSION="9"
        BASE_TAG="9.7-1763362218"
        ;;
    el10)
        BASE_IMAGE="registry.access.redhat.com/ubi10/ubi-minimal:10.1-1769677092"
        EL_VERSION="10"
        BASE_TAG="10.1-1769677092"
        ;;
esac

echo "Building base image for $OS_ID using Containerfile.base"
echo "Base image: $BASE_IMAGE"

IMAGE_REPO=${IMAGE_REPO:-quay.io/flightctl/flightctl-base}

# Get architecture for tagging
arch=$(uname -m)
case $arch in
    x86_64) arch=amd64;;
    aarch64) arch=arm64;;
esac

# Create tags (matching the old script naming)
ARCH_TAG="$OS_ID-$arch-$BASE_TAG"
COMMON_TAG="$OS_ID-$BASE_TAG"

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
