#!/usr/bin/env bash
set -euo pipefail

IMAGE_REPO=${IMAGE_REPO:-quay.io/flightctl/flightctl-base}
IMAGE_TAG=9.7-1762965531

arch=$(uname -m)
case $arch in
    x86_64) arch=amd64;;
    aarch64) arch=arm64;;
esac

container=$(buildah from registry.redhat.io/ubi9-micro:$IMAGE_TAG)

mountdir=$(buildah mount $container)
dnf install \
    --installroot $mountdir \
    --releasever 9 \
    --setopt install_weak_deps=false \
    --nodocs -y \
    openssl-libs tzdata
dnf clean all \
    --installroot $mountdir
buildah umount $container

buildah commit $container $IMAGE_REPO:$arch-$IMAGE_TAG
