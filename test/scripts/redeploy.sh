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

    *) echo "Usage: $0 <api|worker|periodic|alert-exporter|alertmanager-proxy>"
       exit 1
esac

podman tag flightctl-${IMAGE}:latest localhost/flightctl-${IMAGE}:latest
kind_load_image localhost/flightctl-${IMAGE}:latest

# switch for api worker and periodic handling, we need to kill the pods to reload
${OC} delete pod -n ${NAMESPACE} -l flightctl.service=flightctl-${IMAGE}
sleep 5
${OC} logs -f -n ${NAMESPACE} -l flightctl.service=flightctl-${IMAGE}
