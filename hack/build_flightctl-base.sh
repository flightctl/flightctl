#!/usr/bin/env bash
set -euo pipefail

IMAGE_REPO=${IMAGE_REPO:-quay.io/flightctl/flightctl-base}
IMAGE_TAGS=${IMAGE_TAGS:-"9.7-1762965531 10.1-1769518576"}
ARCHES=${ARCHES:-"amd64 arm64"}

CONTAINERS_TO_CLEAN=()
cleanup() {
    for c in "${CONTAINERS_TO_CLEAN[@]}"; do
        buildah umount "$c" 2>/dev/null || true
        buildah rm "$c" 2>/dev/null || true
    done
}
trap cleanup EXIT

for IMAGE_TAG in $IMAGE_TAGS; do
    UBI_MAJOR=${IMAGE_TAG%%.*}
    UBI_BASE=registry.redhat.io/ubi${UBI_MAJOR}-micro:${IMAGE_TAG}

    for arch in $ARCHES; do
        forcearch=$(case $arch in amd64) echo x86_64;; arm64) echo aarch64;; *) echo "$arch";; esac)
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

    buildah manifest rm "$IMAGE_REPO:${IMAGE_TAG}" 2>/dev/null || true
    buildah rmi "$IMAGE_REPO:${IMAGE_TAG}" 2>/dev/null || true
    buildah manifest create "$IMAGE_REPO:${IMAGE_TAG}"
    for arch in $ARCHES; do
        buildah manifest add "$IMAGE_REPO:${IMAGE_TAG}" "$IMAGE_REPO:${arch}-${IMAGE_TAG}"
    done
    buildah manifest push --all "$IMAGE_REPO:${IMAGE_TAG}" "docker://$IMAGE_REPO:${IMAGE_TAG}"
done
