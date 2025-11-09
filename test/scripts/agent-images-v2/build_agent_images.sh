#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# Default values
TAG="${TAG:-latest}"
IMAGE_REPO="${IMAGE_REPO:-quay.io/flightctl/flightctl-device}"
PARALLEL_JOBS="${PARALLEL_JOBS:-4}"
BUILD_MODE="${BUILD_MODE:-all}"
VARIANTS="${VARIANTS:-v2 v3 v4 v5 v6 v8 v9 v10}"

# Version build args
SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$(git describe --tags --exclude latest 2>/dev/null || echo "v0.0.0-unknown")}"
SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE:-$( ( ( [ ! -d ".git/" ] || git diff --quiet ) && echo 'clean' ) || echo 'dirty' )}"
SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT:-$(git rev-parse --short "HEAD^{commit}" 2>/dev/null || echo "unknown")}"

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --mode)
            BUILD_MODE="$2"
            shift 2
            ;;
        --variants)
            VARIANTS="$2"
            shift 2
            ;;
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --mode MODE       Build mode: all, base, variants, qcow2 (default: all)"
            echo "  --variants LIST   Space-separated list of variants to build (default: v2 v3 v4 v5 v6 v8 v9 v10)"
            echo ""
            echo "Environment variables:"
            echo "  TAG               Image tag (default: latest)"
            echo "  IMAGE_REPO        Image repository (default: quay.io/flightctl/flightctl-device)"
            echo "  PARALLEL_JOBS     Number of parallel jobs for variant builds (default: 4)"
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

echo "Build mode: ${BUILD_MODE}"
echo "TAG: ${TAG}"
echo "IMAGE_REPO: ${IMAGE_REPO}"
echo "SOURCE_GIT_TAG: ${SOURCE_GIT_TAG}"
echo "SOURCE_GIT_COMMIT: ${SOURCE_GIT_COMMIT}"

cd "$PROJECT_ROOT"

build_base_image() {
    local image_name="${IMAGE_REPO}:base-${TAG}"

    echo -e "\033[32mBuilding base image ${image_name}\033[m"

    podman build \
        --build-arg SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
        --build-arg SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE}" \
        --build-arg SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT}" \
        -f "${SCRIPT_DIR}/Containerfile-base" \
        -t "${image_name}" \
        .

    echo -e "\033[32mSuccessfully built ${image_name}\033[m"
}

build_variant_image() {
    local variant="$1"
    local base_image="${IMAGE_REPO}:base-${TAG}"
    local variant_image="${IMAGE_REPO}:${variant}-${TAG}"

    echo -e "\033[32mBuilding variant image ${variant_image}\033[m"

    podman build \
        --build-arg BASE_IMAGE="${base_image}" \
        -f "${SCRIPT_DIR}/Containerfile-${variant}" \
        -t "${variant_image}" \
        .

    echo -e "\033[32mSuccessfully built ${variant_image}\033[m"
}

build_variants() {
    if ! [[ "$PARALLEL_JOBS" =~ ^[0-9]+$ ]] || [ "$PARALLEL_JOBS" -lt 1 ]; then
        echo -e "\033[31mError: PARALLEL_JOBS must be a positive integer, got: $PARALLEL_JOBS\033[m"
        PARALLEL_JOBS=1
    fi

    echo -e "\033[33mBuilding variant images in parallel (max $PARALLEL_JOBS jobs)...\033[m"
    echo -e "\033[33mVariants to build: ${VARIANTS}\033[m"

    local job_count=0
    for variant in $VARIANTS; do
        while [ $job_count -ge $PARALLEL_JOBS ]; do
            wait -n
            job_count=$((job_count - 1))
        done
        build_variant_image "$variant" &
        job_count=$((job_count + 1))
    done
    wait

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
    all)
        echo -e "\033[33mBuilding all images...\033[m"
        build_base_image
        build_variants
        build_qcow2_image
        ;;
    *)
        echo -e "\033[31mError: Invalid build mode: $BUILD_MODE\033[m"
        echo "Valid modes: base, variants, qcow2, all"
        exit 1
        ;;
esac

echo -e "\033[32mBuild completed successfully!\033[m"
