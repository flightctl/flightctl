#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
OS_ID="${OS_ID:?OS_ID is required}"
OUTPUT_DIR="${OUTPUT_DIR:-${ROOT_DIR}/bin/output/agent-qcow2-${OS_ID}}"

TAG="${TAG:-$(${ROOT_DIR}/hack/current-version)}"
IMAGE_REPO="${IMAGE_REPO:-quay.io/flightctl/flightctl-device}"
BASE_IMAGE="${IMAGE_REPO}:base-${OS_ID}-${TAG}"

mkdir -p "${OUTPUT_DIR}"
mkdir -p "${ROOT_DIR}/dnf-cache" "${ROOT_DIR}/osbuild-cache"

echo -e "\033[32mProducing qcow2 image for ${BASE_IMAGE}, writing to ${OUTPUT_DIR}\033[m"

sudo podman run --rm \
                -it \
                --privileged \
                --pull=newer \
                --security-opt label=type:unconfined_t \
                -v "${OUTPUT_DIR}":/output \
                -v "${ROOT_DIR}"/dnf-cache:/var/cache/dnf:Z \
                -v "${ROOT_DIR}"/osbuild-cache:/var/cache/osbuild:Z \
                -v /var/lib/containers/storage:/var/lib/containers/storage \
                quay.io/centos-bootc/bootc-image-builder:latest \
                build \
                --type qcow2 \
                "${BASE_IMAGE}"

sudo chown -R "${USER}:$(id -gn ${USER})" "${OUTPUT_DIR}"

echo -e "\033[32mqcow2 image created at ${OUTPUT_DIR}/qcow2/disk.qcow2\033[m"

ls -lh "${OUTPUT_DIR}"




