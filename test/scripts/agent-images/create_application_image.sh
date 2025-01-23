#!/usr/bin/env bash
set -ex
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

source "${SCRIPT_DIR}"/../functions

REGISTRY_ADDRESS=$(registry_address)
IMAGE_LIST="v1 v2"


for img in $IMAGE_LIST; do
   FINAL_REF="${REGISTRY_ADDRESS}/sleep-app:${img}"
   echo -e "\033[32mCreating image ${FINAL_REF} \033[m"
   podman build -f "${SCRIPT_DIR}"/Containerfile-sleep-app-"${img}" -t localhost:5000/sleep-app:${img} .
   podman tag "localhost:5000/sleep-app:${img}" "${FINAL_REF}"
   podman push "${FINAL_REF}"
done

