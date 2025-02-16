#!/usr/bin/env bash
set -ex

BUILD_TYPE=${BUILD_TYPE:-bootc}
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

source "${SCRIPT_DIR}"/../functions

REGISTRY_ADDRESS=$(registry_address)
IMAGE_LIST="base v2 v3 v4 v5"

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

build_images() {
    for img in $IMAGE_LIST; do
        containerfile_path="${SCRIPT_DIR}/Containerfile-e2e-${img}.local"
        container_name="localhost:5000/flightctl-device:${img}"

        if [ "$BUILD_TYPE" = "regular" ]; then
            # create a temporary directory and cleanup on exit
            tmpdir=$(mktemp -d)
            trap 'rm -rf "$tmpdir"' EXIT
            cp "${containerfile_path}" "$tmpdir/Containerfile"
            echo 'CMD ["/usr/bin/flightctl-agent"]' >> "$tmpdir/Containerfile"
            containerfile_path="$tmpdir/Containerfile"
            container_name="localhost:5000/flightctl-device-no-bootc:${img}"
        fi

        FINAL_REF="${REGISTRY_ADDRESS}/$(basename "${container_name}")"

        echo -e "\033[32mCreating image ${FINAL_REF} with BUILD_TYPE=${BUILD_TYPE} \033[m"

        podman build ${BUILD_ARGS:+${BUILD_ARGS}} -f "${containerfile_path}" -t "${container_name}" .
        podman tag "${container_name}" "${FINAL_REF}"
        podman push "${FINAL_REF}"
    done
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