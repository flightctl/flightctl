#!/usr/bin/env bash
set -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

source "${SCRIPT_DIR}"/functions

IMAGE=${1}
OC=${OC:=oc}
OS=${OS:-"el9"}

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

podman tag localhost/flightctl-${IMAGE}-${OS}:latest localhost/flightctl-${IMAGE}-${OS}:latest
kind_load_image localhost/flightctl-${IMAGE}-${OS}:latest

# switch for api worker and periodic handling, we need to kill the pods to reload
${OC} delete pod -n ${NAMESPACE} -l flightctl.service=flightctl-${IMAGE}
sleep 5
${OC} logs -f -n ${NAMESPACE} -l flightctl.service=flightctl-${IMAGE}
