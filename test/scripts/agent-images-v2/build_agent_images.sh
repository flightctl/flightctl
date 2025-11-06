#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

TAG="${TAG:-latest}"
IMAGE_REPO="${IMAGE_REPO:-quay.io/flightctl/flightctl-device}"
PARALLEL_JOBS="${PARALLEL_JOBS:-4}"

# Version build args
SOURCE_GIT_TAG="${SOURCE_GIT_TAG:-$(git describe --tags --exclude latest 2>/dev/null || echo "v0.0.0-unknown")}"
SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE:-$( ( ( [ ! -d ".git/" ] || git diff --quiet ) && echo 'clean' ) || echo 'dirty' )}"
SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT:-$(git rev-parse --short "HEAD^{commit}" 2>/dev/null || echo "unknown")}"

IMAGE_LIST="base v2 v3 v4"

echo "Building agent images with TAG=${TAG}"
echo "SOURCE_GIT_TAG=${SOURCE_GIT_TAG}"
echo "SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT}"

cd "$PROJECT_ROOT"

build_single_image() {
    local img="$1"
    local containerfile_path="${SCRIPT_DIR}/Containerfile-${img}"
    local image_name="${IMAGE_REPO}:${img}-${TAG}"

    echo -e "\033[32mBuilding image ${image_name}\033[m"

    local build_args=""
    build_args="--build-arg SOURCE_GIT_TAG=${SOURCE_GIT_TAG}"
    build_args="${build_args} --build-arg SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE}"
    build_args="${build_args} --build-arg SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT}"

    # For variant images, set BASE_IMAGE
    if [ "$img" != "base" ]; then
        build_args="${build_args} --build-arg BASE_IMAGE=${IMAGE_REPO}:base-${TAG}"
    fi

    podman build ${build_args} \
        -f "${containerfile_path}" \
        -t "${image_name}" \
        .

    echo -e "\033[32mSuccessfully built ${image_name}\033[m"
}

build_images() {
    if ! [[ "$PARALLEL_JOBS" =~ ^[0-9]+$ ]] || [ "$PARALLEL_JOBS" -lt 1 ]; then
        echo -e "\033[31mError: PARALLEL_JOBS must be a positive integer, got: $PARALLEL_JOBS\033[m"
        PARALLEL_JOBS=1
    fi

    echo -e "\033[33mUsing PARALLEL_JOBS=$PARALLEL_JOBS\033[m"

    # Build base image first
    echo -e "\033[33mBuilding base image first...\033[m"
    build_single_image "base"

    # Build remaining images in parallel
    local other_images=$(echo $IMAGE_LIST | sed 's/base//')
    if [ -n "$other_images" ]; then
        echo -e "\033[33mBuilding variant images in parallel (max $PARALLEL_JOBS jobs)...\033[m"
        local job_count=0
        for img in $other_images; do
            while [ $job_count -ge $PARALLEL_JOBS ]; do
                wait -n
                job_count=$((job_count - 1))
            done
            build_single_image "$img" &
            job_count=$((job_count + 1))
        done
        wait
    fi
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

# Build images
build_images

# Build qcow2 if requested
if [ "${BUILD_QCOW2:-true}" = "true" ]; then
    build_qcow2_image
fi

echo -e "\033[32mAll images built successfully!\033[m"

