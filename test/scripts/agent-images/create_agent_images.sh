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

if [ "${BUILD_TYPE}" = "regular" ]; then
    if [ -z "${AGENT_OS_ID:-}" ]; then
        AGENT_OS_ID="cs9-regular"
        echo "BUILD_TYPE=regular defaults AGENT_OS_ID to cs9-regular"
    elif [ "${AGENT_OS_ID}" != "cs9-regular" ]; then
        echo "BUILD_TYPE=regular supports only AGENT_OS_ID=cs9-regular, got ${AGENT_OS_ID}" >&2
        exit 1
    fi
fi

# Handle v7 variant based on OS and RHOCP access
# Only auto-detect if user hasn't explicitly set EXCLUDE_VARIANTS
if [ -z "${EXCLUDE_VARIANTS+x}" ]; then
    if [ "${AGENT_OS_ID:-cs9-bootc}" = "cs10-bootc" ]; then
        export EXCLUDE_VARIANTS="v7"
        echo "cs10: v7 excluded (no RHOCP MicroShift for cs10)"
    elif [ "${AGENT_OS_ID:-cs9-bootc}" = "cs9-regular" ]; then
        export EXCLUDE_VARIANTS="v7 v11 v12"
        echo "cs9-regular: v7, v11, v12 excluded (bootc-specific variants)"
    elif has_rhocp_access; then
        export EXCLUDE_VARIANTS=""
        echo "RHOCP access available, enabling v7"
    else
        export EXCLUDE_VARIANTS="v7"
        echo "No RHOCP access, excluding v7"
    fi
fi

# Determine the RPM suffix used when the selected COPR package name is versioned.
get_os_suffix() {
    local flavor="${1:-cs9-bootc}"
    case "${flavor}" in
        cs9*)  echo ".el9" ;;
        cs10*) echo ".el10" ;;
        *)     echo "" ;;
    esac
}

# Treat the shared qcow output as current only when it matches the requested
# flavor and is newer than the last relevant build input we can observe here.
qcow_is_up_to_date() {
    local os_id="$1"
    local qcow_path="${ROOT_DIR}/bin/output/qcow2/disk.qcow2"
    local qcow_os_id_path="${ROOT_DIR}/bin/output/qcow2/disk.qcow2.os-id"
    local touch_file="${ROOT_DIR}/bin/.e2e-agent-images-${os_id}"

    [[ -f "${qcow_path}" ]] || return 1
    [[ -f "${qcow_os_id_path}" ]] || return 1
    [[ "$(cat "${qcow_os_id_path}")" = "${os_id}" ]] || return 1

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

# Record which OS flavor produced the shared qcow path so later runs do not
# mistake a bootc artifact for a regular one, or vice versa.
write_qcow_os_id_sidecar() {
    local os_id="$1"
    local qcow_dir="${ROOT_DIR}/bin/output/qcow2"

    mkdir -p "${qcow_dir}"
    printf '%s\n' "${os_id}" > "${qcow_dir}/disk.qcow2.os-id"
}

# Build extra flags for RPM source.
# Keep these source selectors initialized here so the OCI build path and the
# regular qcow path consume the same RPM-source inputs when this wrapper is used.
BUILD_ARGS=""
RPM_DIR="${RPM_DIR:-rpm}"
RPM_COPR_REPO="${RPM_COPR_REPO:-}"
RPM_COPR_PACKAGE="${RPM_COPR_PACKAGE:-}"

# Log only the brew host so build logs stay useful without echoing caller-supplied
# URLs verbatim. Some URLs may carry embedded credentials or tokens.
safe_brew_build_url() {
    local brew_url="$1"
    local host

    host="$(printf '%s\n' "${brew_url}" | sed -E 's#^[A-Za-z]+://([^/@]+@)?([^/:?#]+).*#\2#')"
    if [ -z "${host}" ] || [ "${host}" = "${brew_url}" ]; then
        echo "<redacted>"
        return
    fi

    echo "${host}"
}

# Accept only owner/project COPR references before forwarding them into build args.
validate_copr_repo() {
    local copr_repo="$1"
    if [[ ! "${copr_repo}" =~ ^@?[A-Za-z0-9._+-]+/[A-Za-z0-9._+-]+$ ]]; then
        echo "Invalid RPM_COPR_REPO: ${copr_repo}" >&2
        exit 1
    fi
}

# Accept only package-name characters before forwarding the package into build args.
validate_copr_package() {
    local copr_package="$1"
    if [[ ! "${copr_package}" =~ ^[A-Za-z0-9._:+-]+$ ]]; then
        echo "Invalid RPM_COPR_PACKAGE: ${copr_package}" >&2
        exit 1
    fi
}

# Keep local RPM lookups confined to a simple directory name beneath bin/.
validate_rpm_dir() {
    local rpm_dir="$1"
    if [[ ! "${rpm_dir}" =~ ^[A-Za-z0-9._+-]+$ ]] || [[ "${rpm_dir}" == *"/"* ]] || [[ "${rpm_dir}" == *".."* ]]; then
        echo "Invalid RPM_DIR: ${rpm_dir}" >&2
        exit 1
    fi
}

if [ -n "${BREW_BUILD_URL:-}" ]; then
    echo "Using brew registry RPMs from host: $(safe_brew_build_url "${BREW_BUILD_URL}")"

    if ! download_brew_rpms "${ROOT_DIR}/bin/brew-rpm" "${BREW_BUILD_URL}" "flightctl-agent-*" "flightctl-selinux*"; then
        exit 1
    fi

    RPM_DIR="brew-rpm"
    validate_rpm_dir "${RPM_DIR}"
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

    validate_copr_repo "${RPM_COPR_REPO}"
    validate_copr_package "${RPM_COPR_PACKAGE}"

    BUILD_ARGS="--build-arg RPM_COPR_REPO=${RPM_COPR_REPO}"
    BUILD_ARGS="${BUILD_ARGS} --build-arg RPM_COPR_PACKAGE=${RPM_COPR_PACKAGE}"

else
    echo "No BREW_BUILD_URL or FLIGHTCTL_RPM provided, using local RPMs only"
    validate_rpm_dir "${RPM_DIR}"
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
export RPM_DIR
export RPM_COPR_REPO
export RPM_COPR_PACKAGE
# Calculate registry endpoint for pushing (if not already set)
export REGISTRY_ENDPOINT

# Determine OS_ID strictly from AGENT_OS_ID (single source of truth)
AGENT_OS_ID="${AGENT_OS_ID:-cs9-bootc}"
case "${AGENT_OS_ID}" in
    cs9-bootc)   OS_ID="cs9-bootc" ;;
    cs9-regular) OS_ID="cs9-regular" ;;
    cs10*) OS_ID="cs10-bootc" ;;
    *)     OS_ID="${AGENT_OS_ID}" ;;
esac

# Export so downstream scripts see the selected flavor
export AGENT_OS_ID
export OS_ID

build_base() {
    if [ -n "${PODMAN_BUILD_EXTRA_FLAGS}" ]; then
        echo "Building base image with caller-provided podman build extra flags"
    else
        echo "Building base image"
    fi
    sudo -E "${SCRIPT_DIR}/scripts/build.sh" --base
}

build_variants_and_qcow2() {
    echo "Building variants, bundle, and qcow2 for OS_ID=${OS_ID}"
    echo "Registry endpoint for push: ${REGISTRY_ENDPOINT}"

    # Only push if PUSH_IMAGES is set to true
    PUSH_ARG=""
    if [ "${PUSH_IMAGES:-false}" = "true" ]; then
        PUSH_ARG="--push"
    fi

    local skip_qcow="${SKIP_QCOW_BUILD:-false}"
    if [ "${skip_qcow}" != "true" ] && qcow_is_up_to_date "${OS_ID}"; then
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
        write_qcow_os_id_sidecar "${OS_ID}"
        echo "Moved qcow2 to ${QCOW_DST}"

        # Resize disk if v7 is enabled (for MicroShift)
        if [ -z "${EXCLUDE_VARIANTS:-}" ] || ! echo "${EXCLUDE_VARIANTS}" | grep -qw "v7"; then
            echo "v7 enabled, resizing qcow2 disk +5G"
            sudo qemu-img resize "${QCOW_DST}" +5G
        fi

        # Fix permissions on bin/output
        sudo chown -R "${USER}:$(id -gn "${USER}")" "${ROOT_DIR}/bin/output" || true
    fi
}

# Build the package-mode path: regular base image plus qcow2 only, then move the
# resulting qcow into the shared output location and record its OS flavor.
build_regular_qcow2() {
    echo "Building package-mode base and qcow2 for OS_ID=${OS_ID}"
    # Regular (package-mode) builds no longer produce QCOW2 by default; callers
    # that still need a qcow must set SKIP_QCOW_BUILD=false explicitly.
    SKIP_VARIANTS_BUILD=true SKIP_QCOW_BUILD="${SKIP_QCOW_BUILD:-true}" "${SCRIPT_DIR}/scripts/build_and_qcow2.sh" --os-id "${OS_ID}"

    sudo chown -R "${USER}:$(id -gn "${USER}")" "${ROOT_DIR}/artifacts" || true

    OUTPUT_DIR="${OUTPUT_DIR:-${ROOT_DIR}/bin/output/agent-qcow2-${OS_ID}}"
    QCOW_SRC="${ROOT_DIR}/bin/output/agent-qcow2-${OS_ID}/qcow2/disk.qcow2"
    QCOW_DST="${ROOT_DIR}/bin/output/qcow2/disk.qcow2"
    if [ -f "${QCOW_SRC}" ]; then
        mkdir -p "${ROOT_DIR}/bin/output/qcow2"
        mv "${QCOW_SRC}" "${QCOW_DST}"
        write_qcow_os_id_sidecar "${OS_ID}"
        echo "Moved qcow2 to ${QCOW_DST}"
        sudo chown -R "${USER}:$(id -gn "${USER}")" "${ROOT_DIR}/bin/output" || true
    fi
}

case "$BUILD_TYPE" in
    regular)
        build_base
        build_regular_qcow2
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
