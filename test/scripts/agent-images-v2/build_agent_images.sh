#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# Default values
TAG="${TAG:-latest}"
IMAGE_REPO="${IMAGE_REPO:-quay.io/flightctl/flightctl-device}"
SLEEP_APP_REPO="${SLEEP_APP_REPO:-quay.io/flightctl/sleep-app}"
PARALLEL_JOBS="${PARALLEL_JOBS:-4}"
BUILD_MODE="${BUILD_MODE:-all}"
VARIANTS="${VARIANTS:-v2 v3 v4 v5 v6 v8 v9 v10}"
SLEEP_APP_VARIANTS="${SLEEP_APP_VARIANTS:-v1 v2 v3}"

# Version build args
SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$(git describe --tags --exclude latest 2>/dev/null || echo "v0.0.0-unknown")}"
SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE:-$( ( ( [ ! -d ".git/" ] || git diff --quiet ) && echo 'clean' ) || echo 'dirty' )}"
SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT:-$(git rev-parse --short "HEAD^{commit}" 2>/dev/null || echo "unknown")}"

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --verbose|-v)
            VERBOSE=1
            shift
            ;;
        --jobs)
            JOBS="$2"
            shift 2
            ;;
        --mode)
            BUILD_MODE="$2"
            shift 2
            ;;
        --variants)
            VARIANTS="$2"
            shift 2
            ;;
        --sleep-variants)
            SLEEP_APP_VARIANTS="$2"
            shift 2
            ;;
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --mode MODE         Build mode: all, base, variants, sleep-apps, qcow2 (default: all)"
            echo "  --variants LIST     Space-separated list of agent variants to build (default: v2 v3 v4 v5 v6 v8 v9 v10)"
            echo "  --sleep-variants LIST  Space-separated list of sleep app variants to build (default: v1 v2 v3)"
            echo "  --jobs N            Max concurrent variant builds (default: nproc; env: JOBS or PARALLEL_JOBS)"
            echo "  -v, --verbose       Enable verbose output (sets shell -x and debug logging)"
            echo ""
            echo "Environment variables:"
            echo "  TAG               Image tag (default: latest)"
            echo "  IMAGE_REPO        Agent image repository (default: quay.io/flightctl/flightctl-device)"
            echo "  SLEEP_APP_REPO    Sleep app image repository (default: quay.io/flightctl/sleep-app)"
            echo "  JOBS              Max concurrent variant builds (overrides default nproc)"
            echo "  PARALLEL_JOBS     Deprecated alias for JOBS (still honored)"
            echo "  PODMAN_LOG_LEVEL  Podman build log level (debug, info, warn). Default: info"
            echo "  PODMAN_BUILD_EXTRA_FLAGS  Extra flags appended to every podman build (e.g. \"--no-cache --network=host\")"
            echo "  PREBASE_IMAGE     If set, use this image as base for RPM layering (skips prebase build)"
            echo "  SOURCE_GIT_TAG    Git tag for build args"
            echo "  SOURCE_GIT_TREE_STATE  Git tree state for build args"
            echo "  SOURCE_GIT_COMMIT Git commit for build args"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done


DEFAULT_JOBS="$(command -v nproc >/dev/null && nproc || echo 1)"
# Precedence: CLI --jobs > JOBS env > PARALLEL_JOBS env > DEFAULT_JOBS
if [ -z "${JOBS:-}" ]; then
    if [ -n "${PARALLEL_JOBS:-}" ]; then
        JOBS="${PARALLEL_JOBS}"
    else
        JOBS="${DEFAULT_JOBS}"
    fi
fi

if [ "${VERBOSE:-0}" = "1" ]; then
    set -x
fi

echo "Build mode: ${BUILD_MODE}"
echo "TAG: ${TAG}"
echo "IMAGE_REPO: ${IMAGE_REPO}"
echo "SLEEP_APP_REPO: ${SLEEP_APP_REPO}"
echo "SOURCE_GIT_TAG: ${SOURCE_GIT_TAG}"
echo "SOURCE_GIT_COMMIT: ${SOURCE_GIT_COMMIT}"
echo "JOBS: ${JOBS}"

cd "$PROJECT_ROOT"

# Configure cache flags similar to other image-building scripts
if [ "${GITHUB_ACTIONS:-false}" = "true" ]; then
    REGISTRY="${REGISTRY:-}"
    REGISTRY_OWNER_TESTS="${REGISTRY_OWNER_TESTS:-flightctl-tests}"
    if [ -n "${REGISTRY}" ] && [ "${REGISTRY}" != "localhost" ]; then
        CACHE_FLAGS=("--cache-from=${REGISTRY}/${REGISTRY_OWNER_TESTS}/flightctl-device")
    else
        echo "Skipping remote cache-from in CI (no valid REGISTRY configured)"
        CACHE_FLAGS=()
    fi
else
    CACHE_FLAGS=()
fi

PODMAN_LOG_LEVEL="${PODMAN_LOG_LEVEL:-info}"
PODMAN_BUILD_FLAGS=(--jobs "${JOBS}" --log-level "${PODMAN_LOG_LEVEL}" --pull=missing --layers=true --network=host)

build_prebase_image() {
    local image_name="${IMAGE_REPO}:prebase-${TAG}"
    echo -e "\033[32mBuilding prebase image ${image_name}\033[m"
    podman build "${CACHE_FLAGS[@]}" "${PODMAN_BUILD_FLAGS[@]}" ${PODMAN_BUILD_EXTRA_FLAGS:-} \
        --build-arg SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
        --build-arg SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE}" \
        --build-arg SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT}" \
        --target prebase \
        -f "${SCRIPT_DIR}/Containerfile-base" \
        -t "${image_name}" -t "${IMAGE_REPO}:prebase" \
        .
    echo -e "\033[32mSuccessfully built ${image_name}\033[m"
}

build_base_image() {
    local final_image="${IMAGE_REPO}:base-${TAG}"
    local prebase="${PREBASE_IMAGE:-}"

    if [ -z "${prebase}" ]; then
        prebase="${IMAGE_REPO}:prebase-${TAG}"
        echo -e "\033[33mPREBASE_IMAGE not provided, attempting to reuse or pull ${prebase}\033[m"
        if podman image exists "${prebase}" >/dev/null 2>&1 || podman pull "${prebase}" >/dev/null 2>&1; then
            echo -e "\033[33mFound prebase image ${prebase}, will reuse it\033[m"
        else
            echo -e "\033[33mPrebase image ${prebase} not found, building it locally\033[m"
            build_prebase_image
        fi
    else
        echo -e "\033[33mUsing provided PREBASE_IMAGE=${prebase}\033[m"
    fi

    echo -e "\033[32mBuilding final base image ${final_image}\033[m"
    if [ -n "${prebase}" ]; then
        echo -e "\033[33mUsing external PREBASE_IMAGE for base build: ${prebase}\033[m"
        podman build "${CACHE_FLAGS[@]}" "${PODMAN_BUILD_FLAGS[@]}" ${PODMAN_BUILD_EXTRA_FLAGS:-} \
            --build-arg PREBASE_IMAGE="${prebase}" \
            --build-arg SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
            --build-arg SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE}" \
            --build-arg SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT}" \
            --target base-external \
            -f "${SCRIPT_DIR}/Containerfile-base" \
            -t "${final_image}" -t "${IMAGE_REPO}:base" \
            .
    else
        podman build "${CACHE_FLAGS[@]}" "${PODMAN_BUILD_FLAGS[@]}" ${PODMAN_BUILD_EXTRA_FLAGS:-} \
            --build-arg SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
            --build-arg SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE}" \
            --build-arg SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT}" \
            --target base \
            -f "${SCRIPT_DIR}/Containerfile-base" \
            -t "${final_image}" -t "${IMAGE_REPO}:base" \
            .
    fi
    echo -e "\033[32mSuccessfully built ${final_image}\033[m"
}

build_variant_image() {
    local variant="$1"
    local base_image="${IMAGE_REPO}:base-${TAG}"
    local variant_image="${IMAGE_REPO}:${variant}-${TAG}"

    echo -e "\033[32mBuilding variant image ${variant_image}\033[m"

    podman build \
        --build-arg BASE_IMAGE="${base_image}" \
        -f "${SCRIPT_DIR}/Containerfile-${variant}" \
        -t "${variant_image}" -t "${IMAGE_REPO}:${variant}" \
        .

    echo -e "\033[32mSuccessfully built ${variant_image}\033[m"
}

build_variants() {
    echo -e "\033[33mBuilding variant images with xargs (-P ${JOBS})...\033[m"
    echo -e "\033[33mVariants to build: ${VARIANTS}\033[m"

    local base_image="${IMAGE_REPO}:base-${TAG}"

    # Join cache and build flags into strings for safe interpolation
    local CACHE_FLAGS_JOINED=""
    if [ "${#CACHE_FLAGS[@]}" -gt 0 ]; then
        for f in "${CACHE_FLAGS[@]}"; do
            CACHE_FLAGS_JOINED+=" ${f}"
        done
    fi
    local PODMAN_BUILD_FLAGS_JOINED=""
    if [ "${#PODMAN_BUILD_FLAGS[@]}" -gt 0 ]; then
        for f in "${PODMAN_BUILD_FLAGS[@]}"; do
            PODMAN_BUILD_FLAGS_JOINED+=" ${f}"
        done
    fi
    local EXTRA_FLAGS="${PODMAN_BUILD_EXTRA_FLAGS:-}"

    # Export variables used by the xargs command
    export IMAGE_REPO TAG SCRIPT_DIR PROJECT_ROOT base_image CACHE_FLAGS_JOINED PODMAN_BUILD_FLAGS_JOINED EXTRA_FLAGS

    # Build all variants in parallel using xargs
    printf '%s\n' ${VARIANTS} | xargs -n1 -P "${JOBS}" -I {} bash -lc '\
        echo -e "\033[32mBuilding variant image ${IMAGE_REPO}:{}-${TAG}\033[m"; \
        podman build'"${CACHE_FLAGS_JOINED}"' '"${PODMAN_BUILD_FLAGS_JOINED}"' '"${EXTRA_FLAGS}"' \
          --build-arg BASE_IMAGE="${base_image}" \
          -f "${SCRIPT_DIR}/Containerfile-{}" \
          -t "${IMAGE_REPO}:{}-${TAG}" -t "${IMAGE_REPO}:{}" \
          "${PROJECT_ROOT}"'

    echo -e "\033[32mAll variant images built successfully\033[m"
}

build_qcow2_image() {
    local base_image="${IMAGE_REPO}:base-${TAG}"

    echo -e "\033[32mProducing qcow2 image for ${base_image}\033[m"

    mkdir -p output

    # Create cache directories if they don't exist
    mkdir -p "$(pwd)/dnf-cache"
    mkdir -p "$(pwd)/osbuild-cache"

    sudo podman run --rm \
                    -it \
                    --privileged \
                    --pull=newer \
                    --security-opt label=type:unconfined_t \
                    -v "$(pwd)"/output:/output \
                    -v "$(pwd)"/dnf-cache:/var/cache/dnf:Z \
                    -v "$(pwd)"/osbuild-cache:/var/cache/osbuild:Z \
                    -v /var/lib/containers/storage:/var/lib/containers/storage \
                    quay.io/centos-bootc/bootc-image-builder:latest \
                    build \
                    --type qcow2 \
                    "${base_image}"

    sudo chown -R "${USER}:$(id -gn ${USER})" "$(pwd)"/output

    echo -e "\033[32mqcow2 image created at output/qcow2/disk.qcow2\033[m"
}

build_sleep_app() {
    local variant="$1"
    local image_name="${SLEEP_APP_REPO}:${variant}-${TAG}"

    echo -e "\033[32mBuilding sleep app image ${image_name}\033[m"

    podman build "${PODMAN_BUILD_FLAGS[@]}" ${PODMAN_BUILD_EXTRA_FLAGS:-} \
        --build-arg SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
        --build-arg SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE}" \
        --build-arg SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT}" \
        -f "${SCRIPT_DIR}/Containerfile-sleep-app-${variant}" \
        -t "${image_name}" -t "${SLEEP_APP_REPO}:${variant}" \
        .

    echo -e "\033[32mSuccessfully built ${image_name}\033[m"
}

build_sleep_apps() {
    if ! [[ "$PARALLEL_JOBS" =~ ^[0-9]+$ ]] || [ "$PARALLEL_JOBS" -lt 1 ]; then
        echo -e "\033[31mError: PARALLEL_JOBS must be a positive integer, got: $PARALLEL_JOBS\033[m"
        echo -e "\033[33mFalling back to PARALLEL_JOBS=1\033[m"
        PARALLEL_JOBS=1
    fi

    echo -e "\033[33mUsing PARALLEL_JOBS=$PARALLEL_JOBS for parallel sleep app builds\033[m"
    echo -e "\033[33mBuilding sleep app images in parallel (max $PARALLEL_JOBS jobs)...\033[m"
    echo -e "\033[33mSleep app variants to build: ${SLEEP_APP_VARIANTS}\033[m"

    local job_count=0
    for variant in $SLEEP_APP_VARIANTS; do
        while [ $job_count -ge $PARALLEL_JOBS ]; do
            wait -n
            job_count=$((job_count - 1))
        done
        build_sleep_app "$variant" &
        job_count=$((job_count + 1))
    done
    wait

    echo -e "\033[32mAll sleep app images built successfully\033[m"
}

# Execute based on build mode
case "$BUILD_MODE" in
    base)
        echo -e "\033[33mBuilding base image only...\033[m"
        build_base_image
        ;;
    variants)
        echo -e "\033[33mBuilding variant images only...\033[m"
        build_variants
        ;;
    qcow2)
        echo -e "\033[33mBuilding qcow2 image only...\033[m"
        build_qcow2_image
        ;;
    sleep-apps)
        echo -e "\033[33mBuilding sleep app images only...\033[m"
        build_sleep_apps
        ;;
    prebase)
        echo -e "\033[33mBuilding prebase image only...\033[m"
        build_prebase_image
        ;;
    all)
        echo -e "\033[33mBuilding all images...\033[m"
        build_base_image
        build_variants
        build_sleep_apps
        build_qcow2_image
        ;;
    *)
        echo -e "\033[31mError: Invalid build mode: $BUILD_MODE\033[m"
        echo "Valid modes: base, variants, sleep-apps, qcow2, all"
        exit 1
        ;;
esac

echo -e "\033[32mBuild completed successfully!\033[m"
