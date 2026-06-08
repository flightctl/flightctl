#!/usr/bin/env bash
set -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

source "${SCRIPT_DIR}"/functions

IMAGE=${1}
OC=${OC:=oc}

case $IMAGE in
    api)
        NAMESPACE=flightctl-external
        ;;
    worker)
        NAMESPACE=flightctl-internal
        ;;
    periodic)
        NAMESPACE=flightctl-internal
        ;;
    alert-exporter)
        NAMESPACE=flightctl-internal
        ;;
    alertmanager-proxy)
        NAMESPACE=flightctl-internal
        ;;
    telemetry-gateway)
        NAMESPACE=flightctl-external
        ;;
    imagebuilder-worker)
        NAMESPACE=flightctl-internal
        ;;
    imagebuilder-api)
        NAMESPACE=flightctl-external
        ;;


    *) echo "Usage: $0 <api|worker|periodic|alert-exporter|alertmanager-proxy|telemetry-gateway|imagebuilder-worker|imagebuilder-api>"
       exit 1
esac

OS="${OS:-el9}"
SRC_IMAGE="flightctl-${IMAGE}-${OS}:latest"
if ! podman inspect "${SRC_IMAGE}" &>/dev/null; then
  SRC_IMAGE="flightctl-${IMAGE}:latest"
fi

DST_IMAGE="localhost/flightctl-${IMAGE}-${OS}:latest"
podman tag "${SRC_IMAGE}" "${DST_IMAGE}"
kind_load_image "${DST_IMAGE}"

# switch for api worker and periodic handling, we need to kill the pods to reload
${OC} delete pod -n ${NAMESPACE} -l flightctl.service=flightctl-${IMAGE}
sleep 5
${OC} logs -f -n ${NAMESPACE} -l flightctl.service=flightctl-${IMAGE}
