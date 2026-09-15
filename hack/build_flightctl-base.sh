#!/usr/bin/env bash
set -euo pipefail

IMAGE_REPO=${IMAGE_REPO:-quay.io/flightctl/flightctl-base}
UBI_REGISTRY=${UBI_REGISTRY:-registry.access.redhat.com}

# Support two modes:
#   1. CI mode (IMAGE_TAG set): build for native arch only, skip manifest creation.
#      Used by the refresh-base-images GHA workflow which handles multi-arch
#      builds via a matrix strategy and creates manifests in a separate job.
#   2. Local mode (IMAGE_TAGS set or default): build all arches, create manifests.
if [ -n "${IMAGE_TAG:-}" ]; then
    image_tags=("$IMAGE_TAG")
    arch=$(uname -m)
    case $arch in
        x86_64)  arch=amd64;;
        aarch64) arch=arm64;;
        *) echo "ERROR: unsupported architecture: $arch" >&2; exit 1;;
    esac
    arches=("$arch")
    SKIP_MANIFEST=true
else
    if [ -z "${IMAGE_TAGS:-}" ]; then
        echo "ERROR: IMAGE_TAGS must be set in local mode (e.g. IMAGE_TAGS='9.7-1762965531 10.1-1769518576')" >&2
        exit 1
    fi
    read -r -a image_tags <<< "${IMAGE_TAGS}"
    read -r -a arches <<< "${ARCHES:-"amd64 arm64"}"
    SKIP_MANIFEST=${SKIP_MANIFEST:-false}
fi

CONTAINERS_TO_CLEAN=()
cleanup() {
    for c in "${CONTAINERS_TO_CLEAN[@]}"; do
        buildah umount "$c" 2>/dev/null || true
        buildah rm "$c" 2>/dev/null || true
    done
}
trap cleanup EXIT

for IMAGE_TAG in "${image_tags[@]}"; do
    UBI_MAJOR=${IMAGE_TAG%%.*}
    UBI_BASE=${UBI_REGISTRY}/ubi${UBI_MAJOR}/ubi-micro:${IMAGE_TAG}

    for arch in "${arches[@]}"; do
        forcearch=$(case "$arch" in amd64) echo x86_64;; arm64) echo aarch64;; *) echo "$arch";; esac)
        container=$(buildah from --arch "$arch" "$UBI_BASE")
        CONTAINERS_TO_CLEAN+=("$container")
        mountdir=$(buildah mount "$container")
        dnf install --installroot "$mountdir" --releasever "$UBI_MAJOR" \
            --setopt install_weak_deps=false --forcearch "$forcearch" --nodocs -y \
            openssl-libs tzdata
        dnf clean all --installroot "$mountdir"
        buildah umount "$container"
        buildah commit "$container" "$IMAGE_REPO:${arch}-${IMAGE_TAG}"
        buildah rm "$container"
        unset 'CONTAINERS_TO_CLEAN[-1]'
    done

    if [ "$SKIP_MANIFEST" != "true" ]; then
        buildah manifest rm "$IMAGE_REPO:${IMAGE_TAG}" 2>/dev/null || true
        buildah rmi "$IMAGE_REPO:${IMAGE_TAG}" 2>/dev/null || true
        buildah manifest create "$IMAGE_REPO:${IMAGE_TAG}"
        for arch in "${arches[@]}"; do
            buildah manifest add "$IMAGE_REPO:${IMAGE_TAG}" "$IMAGE_REPO:${arch}-${IMAGE_TAG}"
        done
        buildah manifest push --all "$IMAGE_REPO:${IMAGE_TAG}" "docker://$IMAGE_REPO:${IMAGE_TAG}"
    fi
done
