#!/usr/bin/env bash
set -euo pipefail

# Usage: publish_containers.sh <action> <flavor>
# Actions: build, publish
# Flavor: Any flavor defined in deploy/helm/helm-chart-opts.yaml

# Get script directory and load configuration functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/container-config.sh
source "${SCRIPT_DIR}/container-config.sh"

if [[ $# -ne 2 ]]; then
    echo "Usage: $0 <action> <flavor>"
    echo "Actions: build, publish"
    echo "Available flavors:"
    get_available_flavors | sed 's/^/  /'
    echo "Examples:"
    echo "  $0 build el9   # Build containers for EL9"
    echo "  $0 build el10  # Build containers for EL10"
    echo "  $0 publish el9 # Publish containers for EL9"
    exit 1
fi

ACTION="$1"
FLAVOR_PARAM="$2"

# Validate and load flavor configuration
if ! validate_flavor "$FLAVOR_PARAM"; then
    exit 1
fi

if ! load_flavor_config "$FLAVOR_PARAM"; then
    exit 1
fi

# Container services to build/publish
CONTAINER_SERVICES="api pam-issuer worker periodic alert-exporter cli-artifacts userinfo-proxy telemetry-gateway alertmanager-proxy db-setup imagebuilder-api imagebuilder-worker"

# Validate action
case "$ACTION" in
    build|publish)
        ;;
    *)
        echo "Error: Invalid action '$ACTION'. Must be 'build' or 'publish'"
        exit 1
        ;;
esac

# Get git information for publish action
GIT_REF=$(git rev-parse --short HEAD)

case "$ACTION" in
    build)
        echo "Building FlightCtl containers for ${EL_FLAVOR}..."

        # Build each service using the single container build script
        for service in $CONTAINER_SERVICES; do
            "${SCRIPT_DIR}/build_single_container.sh" "${EL_FLAVOR}" "${service}"
        done

        echo "✓ Built all containers for ${EL_FLAVOR}"
        ;;

    publish)
        echo "Publishing FlightCtl containers for ${EL_FLAVOR} to registry..."

        for service in $CONTAINER_SERVICES; do
            local_image="flightctl-${service}:${EL_FLAVOR}-latest"

            echo "Publishing ${local_image}..."

            # Tag and push to registry with flavor-in-tag approach (matching base image pattern)
            podman tag "${local_image}" "quay.io/flightctl/flightctl-${service}:${EL_FLAVOR}-latest"
            podman tag "${local_image}" "quay.io/flightctl/flightctl-${service}:${EL_FLAVOR}-${GIT_REF}"

            # Push with new flavor-in-tag naming (consistent with base image approach)
            podman push "quay.io/flightctl/flightctl-${service}:${EL_FLAVOR}-latest"
            podman push "quay.io/flightctl/flightctl-${service}:${EL_FLAVOR}-${GIT_REF}"
        done

        echo "✓ Published all containers for ${EL_FLAVOR}"
        echo "Registry images: quay.io/flightctl/flightctl-*-${EL_FLAVOR}:latest"
        ;;
esac
