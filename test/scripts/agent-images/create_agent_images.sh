#!/usr/bin/env bash
set -ex

# Wrapper script that handles RPM source detection and calls build.sh and build_and_qcow2.sh
# Behavior matches create_agent_images.sh but uses build.sh and build_and_qcow2.sh internally
# Note: all images are built as root, to use in a non-root context, import with podman load -i bin/agent-images/agent-images-bundle-cs9-bootc.tar


BUILD_TYPE=${BUILD_TYPE:-bootc}
PARALLEL_JOBS="${PARALLEL_JOBS:-4}"
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

source "${SCRIPT_DIR}/../functions"

# Use same defaults as build.sh and build_and_qcow2.sh
SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$(${ROOT_DIR}/hack/current-version)}"
TAG="${TAG:-$SOURCE_GIT_TAG}"
IMAGE_REPO="${IMAGE_REPO:-quay.io/flightctl/flightctl-device}"
REGISTRY_ADDRESS="${REGISTRY_ADDRESS:-$(registry_address)}"
REGISTRY_ENDPOINT="${REGISTRY_ENDPOINT:-$REGISTRY_ADDRESS}"

if ! [[ "${PARALLEL_JOBS}" =~ ^[0-9]+$ ]] || [ "${PARALLEL_JOBS}" -lt 1 ]; then
    echo -e "\033[31mInvalid PARALLEL_JOBS=${PARALLEL_JOBS}, falling back to 1\033[m"
    PARALLEL_JOBS=1
fi

if [ "${PARALLEL_JOBS}" -gt 8 ]; then
    echo -e "\033[33mWarning: PARALLEL_JOBS=${PARALLEL_JOBS} may overwhelm the system\033[m"
fi

export JOBS="${PARALLEL_JOBS}"

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

qcow_is_up_to_date() {
    local os_id="$1"
    local qcow_path="${ROOT_DIR}/bin/output/qcow2/disk.qcow2"
    local touch_file="${ROOT_DIR}/bin/.e2e-agent-images-${os_id}"

    [[ -f "${qcow_path}" ]] || return 1

    if [[ -f "${touch_file}" && "${qcow_path}" -nt "${touch_file}" ]]; then
        return 0
    fi

    local base_image="${IMAGE_REPO}:base-${os_id}-${TAG}"
    local image_created
    image_created=$(podman image inspect --format '{{.Created}}' "${base_image}" 2>/dev/null || true)
    [[ -n "${image_created}" ]] || return 1

    local image_ts qcow_ts
    image_ts=$(date -d "${image_created}" +%s 2>/dev/null || echo 0)
    qcow_ts=$(date -r "${qcow_path}" +%s 2>/dev/null || echo 0)

    if [[ "${image_ts}" -eq 0 || "${qcow_ts}" -eq 0 ]]; then
        return 1
    fi

    if [[ "${qcow_ts}" -ge "${image_ts}" ]]; then
        return 0
    fi

    return 1
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
        OS_SUFFIX=$(get_os_suffix "${AGENT_OS_ID}")
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
# Calculate registry endpoint for pushing (if not already set)
export REGISTRY_ENDPOINT

# Determine OS_ID strictly from AGENT_OS_ID (single source of truth)
AGENT_OS_ID="${AGENT_OS_ID:-cs9-bootc}"
case "${AGENT_OS_ID}" in
    cs9*)  OS_ID="cs9-bootc" ;;
    cs10*) OS_ID="cs10-bootc" ;;
    *)     OS_ID="${AGENT_OS_ID}" ;;
esac

# Export so downstream scripts see the selected flavor
export AGENT_OS_ID
export OS_ID

build_base() {
    echo "Building base image with PODMAN_BUILD_EXTRA_FLAGS: ${PODMAN_BUILD_EXTRA_FLAGS}"
    sudo AGENT_OS_ID="${AGENT_OS_ID}" "${SCRIPT_DIR}/scripts/build.sh" --base
}

build_variants_and_qcow2() {
    echo "Building variants, bundle, and qcow2 for OS_ID=${OS_ID}"
    echo "Registry endpoint for push: ${REGISTRY_ENDPOINT}"

    # Only push if PUSH_IMAGES is set to true
    PUSH_ARG=""
    if [ "${PUSH_IMAGES:-false}" = "true" ]; then
        PUSH_ARG="--push"
    fi

    local skip_qcow="false"
    if qcow_is_up_to_date "${OS_ID}"; then
        echo -e "\033[32mqcow2 artifact for ${OS_ID} is up to date, skipping rebuild\033[m"
        skip_qcow="true"
    fi

    SKIP_QCOW_BUILD="${skip_qcow}" "${SCRIPT_DIR}/scripts/build_and_qcow2.sh" --os-id ${OS_ID} ${PUSH_ARG}

    # Fix permissions on artifacts
    sudo chown -R "${USER}:$(id -gn "${USER}")" "${ROOT_DIR}/artifacts" || true

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
        sudo chown -R "${USER}:$(id -gn "${USER}")" "${ROOT_DIR}/bin/output" || true
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
