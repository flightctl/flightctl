#!/usr/bin/env bash
set -euo pipefail

# Check if we're running in rootless mode and not already in unshare
if [[ $(id -u) != 0 ]] && [[ "${BUILDAH_ISOLATION:-}" != "chroot" ]] && [[ -z "${_CONTAINERS_USERNS_CONFIGURED:-}" ]]; then
    echo "Running in rootless mode, executing within buildah unshare..."
    exec buildah unshare "$0" "$@"
fi

# Default to EL9 if OS_ID not specified
OS_ID=${OS_ID:-el9}

# Source flavor configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
FLAVOR_FILE="$ROOT_DIR/images/flavors/${OS_ID}.conf"

if [[ ! -f "$FLAVOR_FILE" ]]; then
    echo "Error: Flavor configuration not found: $FLAVOR_FILE"
    echo "Available flavors:"
    ls -1 "$ROOT_DIR/images/flavors/" | sed 's/.conf$//' | sed 's/^/  /'
    exit 1
fi

# Load flavor configuration
source "$FLAVOR_FILE"

echo "Building base image for OS: $OS_ID (version $OS_VERSION)"

IMAGE_REPO=${IMAGE_REPO:-quay.io/flightctl/flightctl-base}

arch=$(uname -m)
case $arch in
    x86_64) arch=amd64;;
    aarch64) arch=arm64;;
esac

echo "Creating base image from $UBI_BASE_MICRO:$BASE_IMAGE_TAG"
container=$(buildah from $UBI_BASE_MICRO:$BASE_IMAGE_TAG)

mountdir=$(buildah mount $container)
echo "Installing base packages for release $UBI_RELEASE_VER"
# Clear cache and force refresh for clean package downloads
dnf clean all --installroot $mountdir
# Skip openssl-libs for EL10 due to package header issues - UBI10-micro already includes it
if [[ "$OS_ID" == "el10" ]]; then
    dnf install \
        --installroot $mountdir \
        --releasever $UBI_RELEASE_VER \
        --setopt install_weak_deps=false \
        --setopt keepcache=false \
        --refresh \
        --nodocs --nogpgcheck -y \
        tzdata
else
    dnf install \
        --installroot $mountdir \
        --releasever $UBI_RELEASE_VER \
        --setopt install_weak_deps=false \
        --setopt keepcache=false \
        --refresh \
        --nodocs --nogpgcheck -y \
        openssl-libs tzdata
fi
dnf clean all \
    --installroot $mountdir
buildah umount $container

# Create both architecture-specific and common tags with OS flavor
ARCH_TAG="$OS_ID-$arch-$BASE_IMAGE_TAG"
COMMON_TAG="$OS_ID-$BASE_IMAGE_TAG"

echo "Committing container with tags:"
echo "  - $IMAGE_REPO:$ARCH_TAG (architecture-specific)"
echo "  - $IMAGE_REPO:$COMMON_TAG (common)"

buildah commit $container $IMAGE_REPO:$ARCH_TAG
buildah tag $IMAGE_REPO:$ARCH_TAG $IMAGE_REPO:$COMMON_TAG
