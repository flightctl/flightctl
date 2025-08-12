#!/usr/bin/env bash
set -ex

BUILD_TYPE=${BUILD_TYPE:-bootc}
PARALLEL_JOBS=${PARALLEL_JOBS:-4}
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

source "${SCRIPT_DIR}"/../functions

REGISTRY_ADDRESS=$(registry_address)
IMAGE_LIST="base v2 v3 v4 v5 v6 v8 v9 v10"

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

    podman build ${args:+${args}} -f "${containerfile_path}" -t "${container_name}" .
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
    CONTAINER_CREATE_DATE=$(podman inspect -f '{{.Created}}' ${REGISTRY_ADDRESS}/flightctl-device:base)
    if [[ -f bin/output/qcow2/disk.qcow2 ]]; then
        QCOW_CREATE_DATE=$(date -u -r bin/output/qcow2/disk.qcow2 "+%Y-%m-%d %H:%M:%S")
    fi

    if [[ -n "${QCOW_CREATE_DATE}" && "${CONTAINER_CREATE_DATE}" < "${QCOW_CREATE_DATE}" ]]; then
        echo -e "\033[32mqcow2 is already up to date with the container \033[m"
        return
    fi

    # Pull the image and build the qcow2
    echo -e "\033[32mPulling ${REGISTRY_ADDRESS}/flightctl-device:base to /var/lib/containers/storage\033[m"
    sudo podman pull "${REGISTRY_ADDRESS}/flightctl-device:base"
    echo -e "\033[32m Producing qcow image for ${REGISTRY_ADDRESS}/flightctl-device:base \033[m"
    sudo podman run --rm \
                    -it \
                    --privileged \
                    --pull=newer \
                    --security-opt label=type:unconfined_t \
                    -v "$(pwd)"/bin/output:/output \
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
