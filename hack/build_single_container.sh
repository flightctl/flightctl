#!/usr/bin/env bash
set -euo pipefail

# Usage: build_single_container.sh <flavor> <service>
# Examples:
#   ./build_single_container.sh el9 api
#   ./build_single_container.sh el10 worker

if [[ $# -ne 2 ]]; then
    echo "Usage: $0 <flavor> <service>"
    echo "Flavor: el9, el10"
    echo "Service: api, worker, periodic, etc."
    echo "Examples:"
    echo "  $0 el9 api      # Build API container for EL9"
    echo "  $0 el10 worker  # Build worker container for EL10"
    exit 1
fi

FLAVOR_PARAM="$1"
SERVICE="$2"

# Only accept el9 and el10 flavors
case "$FLAVOR_PARAM" in
    el9) EL_FLAVOR="el9"; EL_VERSION="9" ;;
    el10) EL_FLAVOR="el10"; EL_VERSION="10" ;;
    *)
        echo "Error: Invalid flavor '$FLAVOR_PARAM'. Must be 'el9' or 'el10'"
        exit 1
        ;;
esac

# Validate service name
CONTAINER_SERVICES="api pam-issuer worker periodic alert-exporter cli-artifacts userinfo-proxy telemetry-gateway alertmanager-proxy db-setup imagebuilder-api imagebuilder-worker"
if [[ ! " $CONTAINER_SERVICES " =~ " $SERVICE " ]]; then
    echo "Error: Invalid service '$SERVICE'. Must be one of: $CONTAINER_SERVICES"
    exit 1
fi

# Get script directory and source git information
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
GIT_REF=$(git rev-parse --short HEAD)
SOURCE_GIT_TAG=${SOURCE_GIT_TAG:-$("${ROOT_DIR}"/hack/current-version)}

echo "Building flightctl-${SERVICE} for ${EL_FLAVOR}..."

# Set images and version-specific parameters based on EL_VERSION
case "$EL_VERSION" in
    9)
        BUILD_IMAGE="registry.access.redhat.com/ubi9/go-toolset:1.24.6-1762373805"
        RUNTIME_IMAGE="quay.io/flightctl/flightctl-base:el9-9.7-1762965531"
        MINIMAL_IMAGE="registry.access.redhat.com/ubi9/ubi-minimal:9.7-1763362218"
        PAM_BASE_URL="https://mirror.stream.centos.org/9-stream"
        PAM_PACKAGE_VERSION="1.5.1-24.el9"
        ;;
    10)
        BUILD_IMAGE="registry.access.redhat.com/ubi10/go-toolset:10.1-1770279878"
        RUNTIME_IMAGE="quay.io/flightctl/flightctl-base:el10-10.1-1769518576"
        MINIMAL_IMAGE="registry.access.redhat.com/ubi10/ubi-minimal:10.1-1769677092"
        PAM_BASE_URL="https://mirror.stream.centos.org/10-stream"
        PAM_PACKAGE_VERSION="1.6.1-8.el10"
        ;;
esac

echo "Building flightctl-${SERVICE}-${EL_FLAVOR}..."

# Determine runtime image based on service type
if [[ "$SERVICE" == "cli-artifacts" || "$SERVICE" == "db-setup" ]]; then
    SERVICE_RUNTIME_IMAGE="$MINIMAL_IMAGE"
else
    SERVICE_RUNTIME_IMAGE="$RUNTIME_IMAGE"
fi

# Build with appropriate ARGs
BUILD_ARGS="--build-arg BUILD_IMAGE=${BUILD_IMAGE} --build-arg RUNTIME_IMAGE=${SERVICE_RUNTIME_IMAGE} --build-arg EL_VERSION=${EL_VERSION}"

# Add PAM-specific ARGs for pam-issuer
if [[ "$SERVICE" == "pam-issuer" ]]; then
    BUILD_ARGS="$BUILD_ARGS --build-arg PAM_BASE_URL=${PAM_BASE_URL} --build-arg PAM_PACKAGE_VERSION=${PAM_PACKAGE_VERSION}"
fi

# Build the container
# shellcheck disable=SC2086
podman build $BUILD_ARGS \
    --build-arg SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
    --build-arg SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE:-clean}" \
    --build-arg SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT:-${GIT_REF}}" \
    -f "images/Containerfile.${SERVICE}" \
    -t "flightctl-${SERVICE}:${EL_FLAVOR}-latest" \
    -t "flightctl-${SERVICE}:${EL_FLAVOR}-${SOURCE_GIT_TAG}" \
    -t "quay.io/flightctl/flightctl-${SERVICE}:${EL_FLAVOR}-latest" \
    -t "quay.io/flightctl/flightctl-${SERVICE}:${EL_FLAVOR}-${SOURCE_GIT_TAG}" \
    -t "localhost/flightctl-${SERVICE}:${EL_FLAVOR}-latest" \
    -t "localhost/flightctl-${SERVICE}:${EL_FLAVOR}-${SOURCE_GIT_TAG}" \
    .

echo "✓ Built flightctl-${SERVICE} for ${EL_FLAVOR}"
echo "Local image: flightctl-${SERVICE}:${EL_FLAVOR}-latest"