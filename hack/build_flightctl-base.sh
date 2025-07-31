#!/usr/bin/env bash
set -euo pipefail

IMAGE_TAG=9.6-1752500771

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

buildah commit $container flightctl-base:$IMAGE_TAG
