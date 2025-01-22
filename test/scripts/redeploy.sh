#!/usr/bin/env bash
set -eo pipefail
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

source "${SCRIPT_DIR}"/functions

IMAGE=${1}

case $IMAGE in
    api|worker|periodic)
        ;;

    *) echo "Usage: $0 <api|worker|periodic>"
       exit 1
esac

podman tag flightctl-${IMAGE}:latest localhost/flightctl-${IMAGE}:latest
for suffix in periodic api worker ; do
    kind_load_image localhost/flightctl-${IMAGE}:latest
done

# switch for api worker and periodic handling, we need to kill the pods to reload
case $IMAGE in
    api)
        oc delete pod -n flightctl-external -l flightctl.service=flightctl-api
        sleep 5
        oc logs -f -n flightctl-external -l flightctl.service=flightctl-api
        ;;
    worker)
        oc delete pod -n flightctl-internal -l flightctl.service=flightctl-worker
        sleep 5
        oc logs -f -n flightctl-internal -l flightctl.service=flightctl-worker
        ;;
    periodic)
        oc delete pod -n flightctl-internal -l flightctl.service=flightctl-periodic
        sleep 5
        oc logs -f -n flightctl-internal -l flightctl.service=flightctl-periodic
        ;;
esac

