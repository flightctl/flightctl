#!/usr/bin/env bash
set -euo pipefail

# Usage: publish_containers.sh <action> <flavor>
# Actions: build, publish
# Flavor: Any flavor defined in container-flavors.conf

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

# Get script directory and source git information
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
GIT_REF=$(git rev-parse --short HEAD)
SOURCE_GIT_TAG=${SOURCE_GIT_TAG:-$("${ROOT_DIR}"/hack/current-version)}

case "$ACTION" in
    build)
        echo "Building FlightCtl containers for ${EL_FLAVOR}..."

        # Base images are pre-built and pushed to quay.io/flightctl/flightctl-base
        # No need to build them during container build process

        # Image and version-specific parameters are loaded from container-flavors.conf
        # Variables available: EL_FLAVOR, EL_VERSION, BUILD_IMAGE, RUNTIME_IMAGE, MINIMAL_IMAGE, PAM_BASE_URL, PAM_PACKAGE_VERSION
        echo "Using configuration: BUILD_IMAGE=${BUILD_IMAGE}"
        echo "                    RUNTIME_IMAGE=${RUNTIME_IMAGE}"

        # Build containers using ARG-based containerfiles
        for service in $CONTAINER_SERVICES; do
            echo "Building flightctl-${service}-${EL_FLAVOR}..."

            # Note: GitHub Actions cache is handled by the main build-images-and-charts workflow
            # via Go module and build cache. Container layer caching is not currently configured.

            # Determine runtime image based on service type
            if [[ "$service" == "cli-artifacts" || "$service" == "db-setup" ]]; then
                SERVICE_RUNTIME_IMAGE="$MINIMAL_IMAGE"
            else
                SERVICE_RUNTIME_IMAGE="$RUNTIME_IMAGE"
            fi

            # Build with appropriate ARGs
            BUILD_ARGS="--build-arg BUILD_IMAGE=${BUILD_IMAGE} --build-arg RUNTIME_IMAGE=${SERVICE_RUNTIME_IMAGE} --build-arg EL_VERSION=${EL_VERSION}"

            # Add PAM-specific ARGs for pam-issuer
            if [[ "$service" == "pam-issuer" ]]; then
                BUILD_ARGS="$BUILD_ARGS --build-arg PAM_BASE_URL=${PAM_BASE_URL} --build-arg PAM_PACKAGE_VERSION=${PAM_PACKAGE_VERSION}"
            fi

            # shellcheck disable=SC2086
            podman build $BUILD_ARGS \
                --build-arg SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
                --build-arg SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE:-clean}" \
                --build-arg SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT:-${GIT_REF}}" \
                -f "images/Containerfile.${service}" \
                -t "flightctl-${service}:${EL_FLAVOR}-latest" \
                -t "flightctl-${service}:${EL_FLAVOR}-${SOURCE_GIT_TAG}" \
                -t "quay.io/flightctl/flightctl-${service}:${EL_FLAVOR}-latest" \
                -t "quay.io/flightctl/flightctl-${service}:${EL_FLAVOR}-${SOURCE_GIT_TAG}" \
                -t "localhost/flightctl-${service}:${EL_FLAVOR}-latest" \
                -t "localhost/flightctl-${service}:${EL_FLAVOR}-${SOURCE_GIT_TAG}" \
                .
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
