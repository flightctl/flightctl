#!/usr/bin/env bash
set -ex
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

source "${SCRIPT_DIR}"/../functions

REGISTRY_ADDRESS=$(registry_address)
IMAGE_LIST="base v2 v3 v4"

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



for img in $IMAGE_LIST; do
   FINAL_REF="${REGISTRY_ADDRESS}/flightctl-device:${img}"
   echo -e "\033[32mCreating image ${FINAL_REF} \033[m"
   podman build ${BUILD_ARGS} -f "${SCRIPT_DIR}"/Containerfile-e2e-"${img}".local -t localhost:5000/flightctl-device:${img} .
   podman tag "localhost:5000/flightctl-device:${img}" "${FINAL_REF}"
   podman push "${FINAL_REF}"
done

CONTAINER_CREATE_DATE=$(podman inspect -f '{{.Created}}' ${REGISTRY_ADDRESS}/flightctl-device:base)
if [[ -f bin/output/qcow2/disk.qcow2 ]]; then
	QCOW_CREATE_DATE=$(date -u -r bin/output/qcow2/disk.qcow2  "+%Y-%m-%d %H:%M:%S")
fi 

if [[ "${CONTAINER_CREATE_DATE}" < "${QCOW_CREATE_DATE}" ]]; then
    echo -e "\033[32mqcow2 is already up to date with the contaner \033[m"
    exit 0
fi

echo -e "\033[32mProducing qcow2 image for ${REGISTRY_ADDRESS}/flightctl-device:base \033[m"
mkdir -p bin/output
# pull the image to the root user system storage in /var/lib/containers/storage
echo -e "\033[32mPulling ${REGISTRY_ADDRESS}/flightctl-device:base to /var/lib/containers/storage\033[m"
sudo podman pull "${REGISTRY_ADDRESS}/flightctl-device:base"
echo -e "\033[32m Producing qcow image for ${REGISTRY_ADDRESS}/flightctl-device:base \033[m"
sudo podman run --rm \
                -it \
                --privileged \
                --pull=newer \
                --security-opt label=type:unconfined_t \
                -v $(pwd)/bin/output:/output \
                -v /var/lib/containers/storage:/var/lib/containers/storage \
                quay.io/centos-bootc/bootc-image-builder:latest \
                build \
                --type qcow2 \
                --local "${REGISTRY_ADDRESS}/flightctl-device:base"
