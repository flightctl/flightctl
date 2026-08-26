#!/usr/bin/env bash
set -euo pipefail

# Build the Fedora onboarding device image and its qcow2 disk.
#
# The onboarding e2e suite (GINKGO_LABEL_FILTER=onboarding) needs a device image
# with a usable mac80211_hwsim radio for the WiFi soft-AP specs. That module is
# filtered out of every cs9/cs10 kernel RPM, so the onboarding suite runs on a
# dedicated Fedora bootc image instead (see containerfiles/fedora-bootc/).
#
# This is a deliberately minimal pipeline compared with create_agent_images.sh:
# it builds ONLY the base image and the qcow2 - no v2..v12 variants and no
# multi-image bundle, none of which the onboarding suite uses. It writes the disk
# to the same shared path every suite boots from (bin/output/qcow2/disk.qcow2)
# and records the flavor in the disk.qcow2.os-id sidecar so a later cs9/cs10
# build knows to rebuild rather than reuse this Fedora disk.
#
# Environment:
#   TAG           image tag (defaults to current-version)
#   IMAGE_REPO    device image repo (default quay.io/flightctl/flightctl-device)
#   RPM_COPR_REPO COPR repo for flightctl RPMs (default: Containerfile's
#                 @redhat-et/flightctl-dev). Set to "" plus RPM_DIR to use local
#                 Fedora RPMs instead.
#   RPM_COPR_PACKAGE  COPR agent package name (default: flightctl-agent)

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

OS_ID="fedora-bootc"
export AGENT_OS_ID="${OS_ID}"
export OS_ID

SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$("${ROOT_DIR}/hack/current-version")}"
TAG="${TAG:-$SOURCE_GIT_TAG}"
IMAGE_REPO="${IMAGE_REPO:-quay.io/flightctl/flightctl-device}"
export SOURCE_GIT_TAG TAG IMAGE_REPO

# Forward an optional COPR/local RPM source selection to the Containerfile. The
# Containerfile already defaults RPM_COPR_REPO to @redhat-et/flightctl-dev, so an
# unset RPM_COPR_REPO here means "use that default"; only forward overrides.
BUILD_ARGS=""
if [ -n "${RPM_COPR_REPO+x}" ]; then
    BUILD_ARGS="${BUILD_ARGS} --build-arg RPM_COPR_REPO=${RPM_COPR_REPO}"
fi
if [ -n "${RPM_COPR_PACKAGE:-}" ]; then
    BUILD_ARGS="${BUILD_ARGS} --build-arg RPM_COPR_PACKAGE=${RPM_COPR_PACKAGE}"
fi
if [ -n "${RPM_DIR:-}" ]; then
    BUILD_ARGS="${BUILD_ARGS} --build-arg RPM_DIR=${RPM_DIR}"
fi
if [ -n "${PODMAN_BUILD_EXTRA_FLAGS:-}" ]; then
    export PODMAN_BUILD_EXTRA_FLAGS="${PODMAN_BUILD_EXTRA_FLAGS} ${BUILD_ARGS}"
else
    export PODMAN_BUILD_EXTRA_FLAGS="${BUILD_ARGS}"
fi

# 1) Build the base image (built as root so the qcow2 step can read it from the
#    root podman storage, matching create_agent_images.sh).
echo "Building Fedora onboarding base image (${IMAGE_REPO}:base-${OS_ID}-${TAG})"
sudo -E "${SCRIPT_DIR}/scripts/build.sh" --base

# 2) Produce the qcow2 from the base image. Fedora bootc images do not carry a
#    default root filesystem type, so bootc-image-builder aborts without an
#    explicit --rootfs ("missing required info: DefaultRootFs").
#
#    Use ext4, NOT xfs: the BIB container's mkfs.xfs formats the root with modern
#    on-disk features (nrext64, exchange) that the CI test-vm's CentOS Stream 9
#    host kernel (5.14) cannot mount. Because BIB runs privileged and shares the
#    host kernel, osbuild's install-to-filesystem stage then fails with
#    "mount: wrong fs type, bad option, bad superblock". ext4 has no equivalent
#    kernel-version-gated features and mounts fine under 5.14.
QCOW2_OUTPUT_DIR="${ROOT_DIR}/bin/output/agent-qcow2-${OS_ID}"
echo "Producing qcow2 for ${OS_ID}"
OUTPUT_DIR="${QCOW2_OUTPUT_DIR}" ROOTFS="${ROOTFS:-ext4}" "${SCRIPT_DIR}/scripts/qcow2.sh"

# 3) Move the disk to the shared path every suite boots from and record the flavor.
QCOW_SRC="${QCOW2_OUTPUT_DIR}/qcow2/disk.qcow2"
QCOW_DST="${ROOT_DIR}/bin/output/qcow2/disk.qcow2"
if [ ! -f "${QCOW_SRC}" ]; then
    echo "ERROR: expected qcow2 not found at ${QCOW_SRC}" >&2
    exit 1
fi
mkdir -p "${ROOT_DIR}/bin/output/qcow2"
mv "${QCOW_SRC}" "${QCOW_DST}"
printf '%s\n' "${OS_ID}" > "${ROOT_DIR}/bin/output/qcow2/disk.qcow2.os-id"
sudo chown -R "${USER}:$(id -gn "${USER}")" "${ROOT_DIR}/bin/output" || true

echo "Fedora onboarding qcow2 ready at ${QCOW_DST} (os-id: ${OS_ID})"
