#!/usr/bin/env bash
set -ex

# Wrapper script that handles RPM source detection and calls build.sh and build_and_qcow2.sh
# Behavior matches create_agent_images.sh but uses build.sh and build_and_qcow2.sh internally
# Note: all images are built as root, to use in a non-root context, import with podman load -i bin/agent-images/agent-images-bundle-cs9-bootc.tar


BUILD_TYPE=${BUILD_TYPE:-bootc}
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "${SCRIPT_DIR}/../functions"

# Use same defaults as build.sh and build_and_qcow2.sh
SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$(git describe --tags --exclude latest 2>/dev/null || echo "v0.0.0-unknown")}"
TAG="${TAG:-$SOURCE_GIT_TAG}"
IMAGE_REPO="${IMAGE_REPO:-quay.io/flightctl/flightctl-device}"
FLAVORS="${FLAVORS:-cs9-bootc}"

# Handle ACM detection - enable v7 variant and increase VM memory
if is_acm_installed; then
    export EXCLUDE_VARIANTS=""
    sed -i 's|<memory unit="MiB">512</memory>|<memory unit="MiB">2048</memory>|' test/harness/e2e/vm/domain-template.xml
    echo "ACM detected, enabling v7 variant"
fi

# Determine OS suffix based on flavor
get_os_suffix() {
    local flavor="${1:-cs9-bootc}"
    case "${flavor}" in
        cs9*)  echo ".el9" ;;
        cs10*) echo ".el10" ;;
        *)     echo "" ;;
    esac
}

# Build extra flags for RPM source
BUILD_ARGS=""

if [ -n "${BREW_BUILD_URL:-}" ]; then
    echo "Using BREW_BUILD_URL=${BREW_BUILD_URL} for brew registry RPMs"

    if ! download_brew_rpms "${ROOT_DIR}/bin/brew-rpm" "${BREW_BUILD_URL}" "flightctl-agent-*" "flightctl-selinux*"; then
        exit 1
    fi

    BUILD_ARGS="--build-arg RPM_DIR=brew-rpm"

elif [ -n "${FLIGHTCTL_RPM:-}" ]; then
    echo "Using FLIGHTCTL_RPM=${FLIGHTCTL_RPM} for COPR RPM"

    RPM_COPR_REPO=$(copr_repo)
    RPM_COPR_PACKAGE=$(package_agent)

    # Append OS suffix if versioned
    if [ "${RPM_COPR_PACKAGE}" != "flightctl-agent" ]; then
        OS_SUFFIX=$(get_os_suffix "${FLAVORS}")
        RPM_COPR_PACKAGE="${RPM_COPR_PACKAGE}${OS_SUFFIX}"
    fi

    BUILD_ARGS="--build-arg RPM_COPR_REPO=${RPM_COPR_REPO}"
    BUILD_ARGS="${BUILD_ARGS} --build-arg RPM_COPR_PACKAGE=${RPM_COPR_PACKAGE}"

else
    echo "No BREW_BUILD_URL or FLIGHTCTL_RPM provided, using local RPMs only"
fi

# Merge with any existing PODMAN_BUILD_EXTRA_FLAGS
if [ -n "${PODMAN_BUILD_EXTRA_FLAGS:-}" ]; then
    PODMAN_BUILD_EXTRA_FLAGS="${PODMAN_BUILD_EXTRA_FLAGS} ${BUILD_ARGS}"
else
    PODMAN_BUILD_EXTRA_FLAGS="${BUILD_ARGS}"
fi

export PODMAN_BUILD_EXTRA_FLAGS
export IMAGE_REPO
export TAG
export FLAVORS

# Calculate registry endpoint for pushing (if not already set)
if [ -z "${REGISTRY_ENDPOINT:-}" ]; then
    REGISTRY_ENDPOINT=$(registry_address)
fi
export REGISTRY_ENDPOINT

# Get OS_ID from first flavor
first_flavor="${FLAVORS%% *}"
case "${first_flavor}" in
    cs9*)  OS_ID="cs9-bootc" ;;
    cs10*) OS_ID="cs10-bootc" ;;
    *)     OS_ID="${first_flavor}" ;;
esac
export OS_ID

build_base() {
    echo "Building base image with PODMAN_BUILD_EXTRA_FLAGS: ${PODMAN_BUILD_EXTRA_FLAGS}"
    sudo "${SCRIPT_DIR}/scripts/build.sh" --base
}

build_variants_and_qcow2() {
    echo "Building variants, bundle, and qcow2 for OS_ID=${OS_ID}"
    echo "Registry endpoint for push: ${REGISTRY_ENDPOINT}"

    # Only push if PUSH_IMAGES is set to true
    PUSH_ARG=""
    if [ "${PUSH_IMAGES:-false}" = "true" ]; then
        PUSH_ARG="--push"
    fi

    "${SCRIPT_DIR}/scripts/build_and_qcow2.sh" --os-id ${OS_ID} ${PUSH_ARG}

    # Fix permissions on artifacts
    sudo chown -R "${USER}:$(id -gn ${USER})" "${ROOT_DIR}/artifacts" || true

    # Move qcow2 to bin/output like original script
    OUTPUT_DIR="${OUTPUT_DIR:-${ROOT_DIR}/bin/output/agent-qcow2-${OS_ID}}"
    QCOW_SRC="${ROOT_DIR}/bin/output/agent-qcow2-${OS_ID}/qcow2/disk.qcow2"
    QCOW_DST="${ROOT_DIR}/bin/output/qcow2/disk.qcow2"
    if [ -f "${QCOW_SRC}" ]; then
        mkdir -p "${ROOT_DIR}/bin/output/qcow2"
        mv "${QCOW_SRC}" "${QCOW_DST}"
        echo "Moved qcow2 to ${QCOW_DST}"

        # Resize disk for ACM if installed
        if is_acm_installed; then
            echo "ACM detected, resizing qcow2 disk +5G"
            sudo qemu-img resize "${QCOW_DST}" +5G
        fi

        # Fix permissions on bin/output
        sudo chown -R "${USER}:$(id -gn ${USER})" "${ROOT_DIR}/bin/output" || true
    fi
}

case "$BUILD_TYPE" in
    regular)
        build_base
        ;;
    bootc)
        build_base
        build_variants_and_qcow2
        ;;
    *)
        echo "Unknown BUILD_TYPE: $BUILD_TYPE"
        exit 1
        ;;
esac
