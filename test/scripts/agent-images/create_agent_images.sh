#!/usr/bin/env bash
set -e
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
IP=$("${SCRIPT_DIR}"/../get_ext_ip.sh)


IMAGE_LIST="base v2 v3"

for img in $IMAGE_LIST; do
   echo -e "\033[32mCreating image "${IP}:5000/flightctl-device:${img}" \033[m"
   podman build -f "${SCRIPT_DIR}"/Containerfile-e2e-"${img}".local -t localhost:5000/flightctl-device:${img} .
   podman tag "localhost:5000/flightctl-device:${img}" "${IP}:5000/flightctl-device:${img}"
   podman push "${IP}:5000/flightctl-device:${img}"
done

CONTAINER_CREATE_DATE=$(podman inspect -f '{{.Created}}' ${IP}:5000/flightctl-device:base)
if [[ -f bin/output/qcow2/disk.qcow2 ]]; then
	QCOW_CREATE_DATE=$(date -u -r bin/output/qcow2/disk.qcow2  "+%Y-%m-%d %H:%M:%S")
fi 

if [[ "${CONTAINER_CREATE_DATE}" < "${QCOW_CREATE_DATE}" ]]; then
    echo -e "\033[32mqcow2 is already up to date with the contaner \033[m"
    exit 0
fi

echo -e "\033[32mProducing qcow2 image for ${IP}:5000/flightctl-device:base \033[m"
mkdir -p bin/output
# pull the image to the root user system storage in /var/lib/containers/storage
echo -e "\033[32mPulling ${IP}:5000/flightctl-device:base to /var/lib/containers/storage\033[m"
sudo podman pull "${IP}:5000/flightctl-device:base"
echo -e "\033[32m Producing qcow image for ${IP}:5000/flightctl-device:base \033[m"
sudo podman run --rm \
                -it \
                --privileged \
                --pull=newer \
                --security-opt label=type:unconfined_t \
                -v $(pwd)/bin/output:/output \
                -v /var/lib/containers/storage:/var/lib/containers/storage \
                quay.io/centos-bootc/bootc-image-builder:latest \
                --type qcow2 \
                --local "${IP}:5000/flightctl-device:base"
