#!/usr/bin/env bash

set -euo pipefail

# This script is used to generate the list of endpoints necessary for E2E testing
#
# inputs:
#
# - FLIGHTCTL_NS = namespace where flightctl external services are installed
# - KUBECONFIG = path to kubeconfig file, otherwise the default is used
# - KUBETCL_ARGS = extra arguments to kubectl (i.e. context selection, etc.)

FLIGHTCTL_NS=${FLIGHTCTL_NS:-flightctl-external}
KUBECTL_ARGS=${KUBECTL_ARGS:-}

SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"

IP=$("${SCRIPT_DIR}"/get_ext_ip.sh)

function get_endpoint_host() {
    local name=$1
    kubectl get route "${name}" -n "${FLIGHTCTL_NS}" -o jsonpath='{.spec.host}' ${KUBECTL_ARGS} 2>/dev/null || \
    kubectl get ingress "${name}" -n "${FLIGHTCTL_NS}" -o jsonpath='{.items[0].spec.rules[0].host}' ${KUBECTL_ARGS} 2>/dev/null || \

    # if we cannot find the route or ingress, we assume this is a kind based deployment, and we use the
    # nodeport services instead pointing to our local IP address
    case "${name}" in
        flightctl-api-route)
            echo "api.${IP}.nip.io:3443"
            ;;
        flightctl-api-route-agent)
            echo "agent-api.${IP}.nip.io:7443"
            ;;
        flightctl-api-route-agent-grpc)
            echo "agent-grpc.${IP}.nip.io:7444"
            ;;
        flightctl-ui)
            echo "ui.${IP}.nip.io:7444"
            ;;
        *)
            echo "Unable to find endpoint for ${name}" >&2
            exit 1
            ;;
    esac
}

echo export FLIGHTCTL_API_ENDPOINT=https://$(get_endpoint_host "flightctl-api-route")
echo export FLIGHTCTL_AGENT_ENDPOINT=https://$(get_endpoint_host "flightctl-api-route-agent")
echo export FLIGHTCTL_AGENT_GRPC=grpcs://$(get_endpoint_host "flightctl-api-route-agent-grpc")
echo export FLIGHTCTL_UI_ENDPOINT=https://$(get_endpoint_host "flightctl-ui")


