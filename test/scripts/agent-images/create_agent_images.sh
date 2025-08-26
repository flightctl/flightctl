#!/usr/bin/env bash
set -ex

BUILD_TYPE=${BUILD_TYPE:-bootc}
PARALLEL_JOBS=${PARALLEL_JOBS:-4}
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

source "${SCRIPT_DIR}"/../functions

REGISTRY_ADDRESS=$(registry_address)
IMAGE_LIST="base v2 v3 v4 v5 v6 v8 v9"

if is_acm_installed; then
    IMAGE_LIST="${IMAGE_LIST} v7"
    sed -i 's|<memory unit="MiB">512</memory>|<memory unit="MiB">2048</memory>|' test/harness/e2e/vm/domain-template.xml # increate the memory only for microshift cluster registration test
    echo "IMAGE_LIST=${IMAGE_LIST}"
fi

# if FLIGHTCTL_RPM is not empty
if [ -n "${FLIGHTCTL_RPM:-}" ]; then
    RPM_COPR=$(copr_repo)
    RPM_PACKAGE=$(package_agent)
    # if the package reference includes version, we need to append the system variant, always el9 for our images
    if [[ "${RPM_PACKAGE}" != "flightctl-agent" ]]; then
        RPM_PACKAGE="${RPM_PACKAGE}.el9"
    fi
    BUILD_ARGS="--build-arg=RPM_COPR=${RPM_COPR}"
    BUILD_ARGS="${BUILD_ARGS} --build-arg=RPM_PACKAGE=${RPM_PACKAGE}"
fi

build_single_image() {
    local img="$1"
    containerfile_path="${SCRIPT_DIR}/Containerfile-e2e-${img}.local"
    container_name="localhost:5000/flightctl-device:${img}"

    if [ "$BUILD_TYPE" = "regular" ]; then
        # create a temporary directory and cleanup on exit
        tmpdir=$(mktemp -d)
        trap 'rm -rf "$tmpdir"' RETURN
        cp "${containerfile_path}" "$tmpdir/Containerfile"
        printf '\nCMD ["/usr/bin/flightctl-agent"]\n' >> "$tmpdir/Containerfile"
        containerfile_path="$tmpdir/Containerfile"
        container_name="localhost:5000/flightctl-device-no-bootc:${img}"
    fi

    FINAL_REF="${REGISTRY_ADDRESS}/$(basename "${container_name}")"

    echo -e "\033[32mCreating image ${FINAL_REF} with BUILD_TYPE=${BUILD_TYPE} \033[m"

    # apply image specific args here
    local args="$BUILD_ARGS"
    if [ "$img" = "base" ]; then
      args="${args:+${args} }--build-arg=REGISTRY_ADDRESS=${REGISTRY_ADDRESS}"
    fi

    # Add standard build arguments for caching and versioning
    args="${args:+${args} }--build-arg=SOURCE_GIT_TAG=${SOURCE_GIT_TAG:-$(git describe --tags --exclude latest 2>/dev/null || echo "latest")}"
    args="${args:+${args} }--build-arg=SOURCE_GIT_TREE_STATE=${SOURCE_GIT_TREE_STATE:-$( ( ( [ ! -d ".git/" ] || git diff --quiet ) && echo 'clean' ) || echo 'dirty' )}"
    args="${args:+${args} }--build-arg=SOURCE_GIT_COMMIT=${SOURCE_GIT_COMMIT:-$(git rev-parse --short "HEAD^{commit}" 2>/dev/null || echo "unknown")}"

    # Use GitHub Actions cache when GITHUB_ACTIONS=true, otherwise no caching
    if [ "${GITHUB_ACTIONS:-false}" = "true" ]; then
        REGISTRY="${REGISTRY:-localhost}"
        REGISTRY_OWNER="${REGISTRY_OWNER:-flightctl}"
        CACHE_FLAGS=("--cache-from=${REGISTRY}/${REGISTRY_OWNER}/flightctl-device")
    else
        CACHE_FLAGS=()
    fi

    podman build ${args:+${args}} "${CACHE_FLAGS[@]}" -f "${containerfile_path}" -t "${container_name}" .
    podman tag "${container_name}" "${FINAL_REF}"
    podman push "${FINAL_REF}"
}

build_images() {
    # Validate PARALLEL_JOBS parameter
    if ! [[ "$PARALLEL_JOBS" =~ ^[0-9]+$ ]] || [ "$PARALLEL_JOBS" -lt 1 ]; then
        echo -e "\033[31mError: PARALLEL_JOBS must be a positive integer, got: $PARALLEL_JOBS\033[m"
        echo -e "\033[33mFalling back to PARALLEL_JOBS=1\033[m"
        PARALLEL_JOBS=1
    fi

    # Warn if PARALLEL_JOBS is set too high
    if [ "$PARALLEL_JOBS" -gt 8 ]; then
        echo -e "\033[33mWarning: PARALLEL_JOBS is set to $PARALLEL_JOBS, which may overwhelm the system\033[m"
        echo -e "\033[33mConsider using a lower value (1-8) for better performance\033[m"
    fi

    echo -e "\033[33mUsing PARALLEL_JOBS=$PARALLEL_JOBS for parallel image builds\033[m"

    # Build base image first (required by others)
    echo -e "\033[33mBuilding base image first...\033[m"
    build_single_image "base"

    # Build remaining images in parallel
    local other_images=$(echo $IMAGE_LIST | sed 's/base//')
    if [ -n "$other_images" ]; then
        echo -e "\033[33mBuilding remaining images in parallel (max $PARALLEL_JOBS jobs)...\033[m"
        local job_count=0
        for img in $other_images; do
            while [ $job_count -ge $PARALLEL_JOBS ]; do
                wait -n  # Wait for any job to complete
                job_count=$((job_count - 1))
            done
            build_single_image "$img" &
            job_count=$((job_count + 1))
        done
        wait  # Wait for all remaining jobs to complete
    fi
}

build_qcow2_image() {
    echo -e "\033[32mProducing qcow2 image for ${REGISTRY_ADDRESS}/flightctl-device:base \033[m"

    mkdir -p bin/output
    
    # Check if qcow2 is already up to date
    # Also check if the touch file is newer than the qcow2 (indicating a forced rebuild)
    if [[ -f bin/output/qcow2/disk.qcow2 ]] && [[ -f bin/.e2e-agent-images ]]; then
        TOUCH_FILE_DATE=$(date -u -r bin/.e2e-agent-images "+%Y-%m-%d %H:%M:%S")
        QCOW_FILE_DATE=$(date -u -r bin/output/qcow2/disk.qcow2 "+%Y-%m-%d %H:%M:%S")
        
        # Convert to timestamps for comparison
        TOUCH_TIMESTAMP=$(date -d "${TOUCH_FILE_DATE}" +%s 2>/dev/null || echo "0")
        QCOW_FILE_TIMESTAMP=$(date -d "${QCOW_FILE_DATE}" +%s 2>/dev/null || echo "0")
        
        # If qcow2 is newer than the touch file, we can skip rebuilding
        if [[ ${QCOW_FILE_TIMESTAMP} -gt ${TOUCH_TIMESTAMP} ]]; then
            echo -e "\033[32mqcow2 is newer than touch file, skipping rebuild (touch: ${TOUCH_FILE_DATE}, qcow2: ${QCOW_FILE_DATE})\033[m"
            return
        fi
    fi
    
    if [[ -f bin/output/qcow2/disk.qcow2 ]]; then
        # Get container image ID and creation date
        CONTAINER_ID=$(podman images --format "table {{.ID}}" --noheading ${REGISTRY_ADDRESS}/flightctl-device:base 2>/dev/null | head -1)
        if [[ -n "${CONTAINER_ID}" ]]; then
            CONTAINER_CREATE_DATE=$(podman inspect -f '{{.Created}}' ${CONTAINER_ID} 2>/dev/null || echo "")
            if [[ -n "${CONTAINER_CREATE_DATE}" ]]; then
                QCOW_CREATE_DATE=$(date -u -r bin/output/qcow2/disk.qcow2 "+%Y-%m-%d %H:%M:%S")
                
                # Convert dates to timestamps for proper comparison
                # Handle the container date format: "2025-07-30 16:40:33.146810998 +0000 UTC"
                CONTAINER_DATE_CLEAN=$(echo "${CONTAINER_CREATE_DATE}" | sed 's/\.[0-9]* +0000 UTC//')
                CONTAINER_TIMESTAMP=$(date -d "${CONTAINER_DATE_CLEAN}" +%s 2>/dev/null || echo "0")
                QCOW_TIMESTAMP=$(date -d "${QCOW_CREATE_DATE}" +%s 2>/dev/null || echo "0")
                
                if [[ ${QCOW_TIMESTAMP} -gt ${CONTAINER_TIMESTAMP} ]]; then
                    echo -e "\033[32mqcow2 is already up to date with the container (container: ${CONTAINER_CREATE_DATE}, qcow2: ${QCOW_CREATE_DATE})\033[m"
                    return
                else
                    echo -e "\033[33mqcow2 is older than container, rebuilding (container: ${CONTAINER_CREATE_DATE}, qcow2: ${QCOW_CREATE_DATE})\033[m"
                fi
            else
                echo -e "\033[33mCould not get container creation date, rebuilding\033[m"
            fi
        else
            echo -e "\033[33mContainer image not found, rebuilding\033[m"
        fi
    else
        echo -e "\033[33mqcow2 file not found, building\033[m"
    fi

    # Pull the image and build the qcow2
    echo -e "\033[32mPulling ${REGISTRY_ADDRESS}/flightctl-device:base to /var/lib/containers/storage\033[m"
    sudo podman pull "${REGISTRY_ADDRESS}/flightctl-device:base"
    echo -e "\033[32m Producing qcow image for ${REGISTRY_ADDRESS}/flightctl-device:base \033[m"
    
    # Create cache directories if they don't exist
    mkdir -p "$(pwd)/bin/dnf-cache"
    mkdir -p "$(pwd)/bin/osbuild-cache"
    
    sudo podman run --rm \
                    -it \
                    --privileged \
                    --pull=newer \
                    --security-opt label=type:unconfined_t \
                    -v "$(pwd)"/bin/output:/output \
                    -v "$(pwd)"/bin/dnf-cache:/var/cache/dnf:Z \
                    -v "$(pwd)"/bin/osbuild-cache:/var/cache/osbuild:Z \
                    -v /var/lib/containers/storage:/var/lib/containers/storage \
                    quay.io/centos-bootc/bootc-image-builder:latest \
                    build \
                    --type qcow2 \
                    --local "${REGISTRY_ADDRESS}/flightctl-device:base"
    if is_acm_installed; then
        sudo qemu-img resize "$(pwd)"/bin/output/qcow2/disk.qcow2 +5G # increasing disk size for microshift registration to acm test only
    fi
    # Reset the owner to the user running make
    sudo chown -R "${USER}:$(id -gn ${USER})" "$(pwd)"/bin/output
}

case "$BUILD_TYPE" in
    regular)
        build_images
        ;;
    bootc)
        build_images
        build_qcow2_image
        ;;
    *)
        echo "Unknown BUILD_TYPE: $BUILD_TYPE"
        exit 1
        ;;
esac
