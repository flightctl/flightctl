#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
OS_ID="${OS_ID:?OS_ID is required}"
OUTPUT_DIR="${OUTPUT_DIR:-${ROOT_DIR}/bin/output/agent-qcow2-${OS_ID}}"

TAG="${TAG:-$(${ROOT_DIR}/hack/current-version)}"
IMAGE_REPO="${IMAGE_REPO:-quay.io/flightctl/flightctl-device}"
BASE_IMAGE="${IMAGE_REPO}:base-${OS_ID}-${TAG}"

if [[ "${EUID}" -eq 0 ]]; then
    BIB_IMAGE="${BIB_IMAGE:-ghcr.io/osbuild/bootc-image-builder:v83.0.0}"
    PODMAN_STORAGE="/var/lib/containers/storage"
    BIB_CACHE_ROOT="${ROOT_DIR}"
else
    # The native image-builder CLI in this pinned image supports rootless
    # bootc builds. Its bootc-image-builder compatibility command does not
    # expose --in-vm, so rootless invocations use the native CLI by argv[0].
    BIB_IMAGE="${BIB_IMAGE:-ghcr.io/osbuild/bootc-image-builder@sha256:e7aadce6b3f5639cd47d83354791931ea219891a0d113c2fe74a0f0d352b165c}"
    if ! command -v podman >/dev/null 2>&1; then
        echo "ERROR: Rootless qcow2 builds require Podman for the current user." >&2
        exit 1
    fi

    if [[ -n "${CONTAINER_HOST:-}" || -n "${CONTAINER_CONNECTION:-}" ]]; then
        echo "ERROR: Rootless qcow2 builds require the local Podman store. Unset CONTAINER_HOST and CONTAINER_CONNECTION, then retry." >&2
        exit 1
    fi

    ROOTLESS_PODMAN_CONTEXT="$(podman info --format '{{.Host.Security.Rootless}} {{.Host.ServiceIsRemote}}' 2>/dev/null || true)"
    if [[ "${ROOTLESS_PODMAN_CONTEXT}" != "true false" ]]; then
        echo "ERROR: Rootless qcow2 builds require the invoking user's local rootless Podman service." >&2
        echo "Configure local Podman and verify subordinate UID/GID ranges; qcow2 preparation will not use sudo or a remote image store." >&2
        exit 1
    fi

    if [[ ! -c /dev/kvm || ! -r /dev/kvm || ! -w /dev/kvm ]]; then
        echo "ERROR: Rootless image-builder --in-vm requires readable and writable /dev/kvm access for user $(id -un)." >&2
        echo "Grant access through the KVM group or a per-user device ACL. Refresh the login session only when changing group membership; an ACL may require administrator or udev configuration." >&2
        exit 1
    fi

    PODMAN_STORAGE="$(podman info --format '{{.Store.GraphRoot}}' 2>/dev/null || true)"
    if [[ -z "${PODMAN_STORAGE}" || ! -d "${PODMAN_STORAGE}" ]]; then
        echo "ERROR: Could not locate the current user's Podman image store." >&2
        exit 1
    fi

    # The compatibility CLI and native image-builder CLI expose different
    # flags. Verify the native entrypoint has the rootless bootc arguments
    # before starting a potentially expensive build.
    if ! IMAGE_BUILDER_HELP="$(podman run --rm --pull=newer --entrypoint /bin/sh "${BIB_IMAGE}" -c 'set -eu; ln -sf "$(command -v bootc-image-builder)" /tmp/image-builder; exec /tmp/image-builder build --help' 2>&1)"; then
        echo "ERROR: Could not inspect the native image-builder CLI in ${BIB_IMAGE} with the current user's Podman runtime." >&2
        echo "Check the image reference, registry access, and Podman pull output, then retry." >&2
        exit 1
    fi
    for flag in --in-vm --bootc-ref --bootc-default-fs --output-dir --output-name --cache --rpmmd-cache; do
        if ! grep -q -- "${flag}" <<< "${IMAGE_BUILDER_HELP}"; then
            echo "ERROR: ${BIB_IMAGE} does not expose native image-builder build ${flag}." >&2
            echo "Rootless qcow2 builds require the unified image-builder CLI; rootless mode will not fall back to the compatibility command or host privilege." >&2
            exit 1
        fi
    done

    # Keep unprivileged caches separate from caches that may have been created
    # by rootful builds. The image-builder container writes these directories,
    # and rootless Podman maps container root back to the invoking user.
    BIB_CACHE_ROOT="${ROOT_DIR}/bin/rootless-bib-cache"
fi

DNF_CACHE="${BIB_CACHE_ROOT}/dnf-cache"
OSBUILD_CACHE="${BIB_CACHE_ROOT}/osbuild-cache"
mkdir -p "${OUTPUT_DIR}" "${DNF_CACHE}" "${OSBUILD_CACHE}"

echo -e "\033[32mProducing qcow2 image for ${BASE_IMAGE}, writing to ${OUTPUT_DIR}\033[m"

# The compatibility CLI's --rootfs option and the native CLI's
# --bootc-default-fs option supply a filesystem default when the source image
# has none. CentOS Stream bootc images carry a default; Fedora bootc images do
# not, so QCOW preparation callers building Fedora set ROOTFS explicitly.
BIB_EXTRA_ARGS=()
if [ -n "${ROOTFS:-}" ]; then
    BIB_EXTRA_ARGS+=(--rootfs "${ROOTFS}")
fi

PODMAN_RUN_ARGS=(run --rm --privileged --pull=newer --security-opt label=type:unconfined_t)
if [[ "${EUID}" -eq 0 ]]; then
    PODMAN_RUN_ARGS+=(
        -it
        -v "${OUTPUT_DIR}":/output
        -v "${DNF_CACHE}":/var/cache/dnf:Z
        -v "${OSBUILD_CACHE}":/var/cache/osbuild:Z
        -v "${PODMAN_STORAGE}":/var/lib/containers/storage
    )
    BIB_ARGS=(build --type qcow2 "${BIB_EXTRA_ARGS[@]}" "${BASE_IMAGE}")
else
    PODMAN_RUN_ARGS+=(
        --group-add keep-groups
        --device /dev/kvm
        --entrypoint /bin/sh
        -v "${OUTPUT_DIR}":/output:Z
        -v "${DNF_CACHE}":/var/cache/dnf:Z
        -v "${OSBUILD_CACHE}":/var/cache/osbuild:Z
        -v "${PODMAN_STORAGE}":/var/lib/containers/storage
    )
    BIB_ARGS=(
        -c 'set -eu; ln -sf "$(command -v bootc-image-builder)" /tmp/image-builder; exec /tmp/image-builder "$@"'
        flightctl-image-builder
        build
        --in-vm
        --bootc-ref "${BASE_IMAGE}"
        --output-dir /output/qcow2
        --output-name disk
        --cache /var/cache/osbuild
        --rpmmd-cache /var/cache/dnf
    )
    if [[ -n "${ROOTFS:-}" ]]; then
        BIB_ARGS+=(--bootc-default-fs "${ROOTFS}")
    fi
    BIB_ARGS+=(qcow2)
fi

podman "${PODMAN_RUN_ARGS[@]}" "${BIB_IMAGE}" "${BIB_ARGS[@]}"

if [[ "${EUID}" -eq 0 && -n "${SUDO_UID:-}" && "${SUDO_UID}" != "0" ]]; then
    chown -R "${SUDO_UID}:${SUDO_GID:-${SUDO_UID}}" "${OUTPUT_DIR}"
fi

echo -e "\033[32mqcow2 image created at ${OUTPUT_DIR}/qcow2/disk.qcow2\033[m"

ls -lh "${OUTPUT_DIR}"
