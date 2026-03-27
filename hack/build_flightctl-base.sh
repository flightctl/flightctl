#!/usr/bin/env bash
set -euo pipefail

RELEASE_VERSION=${RELEASE_VERSION:-9}
IMAGE_TAG=${IMAGE_TAG:-9.7-1762965531}
IMAGE_REPO=${IMAGE_REPO:-quay.io/flightctl/flightctl-base-el${RELEASE_VERSION}}

arch=$(uname -m)
case $arch in
    x86_64) arch=amd64;;
    aarch64) arch=arm64;;
esac

container=$(buildah from "registry.access.redhat.com/ubi${RELEASE_VERSION}/ubi-micro:${IMAGE_TAG}")

mountdir=$(buildah mount "$container")
dnf install \
    --installroot "$mountdir" \
    --releasever "${RELEASE_VERSION}" \
    --setopt install_weak_deps=false \
    --nodocs -y \
    openssl-libs tzdata
dnf clean all \
    --installroot "$mountdir"
buildah umount "$container"

buildah commit "$container" "${IMAGE_REPO}:${arch}-${IMAGE_TAG}"
buildah rm "$container"
