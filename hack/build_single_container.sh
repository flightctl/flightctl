#!/usr/bin/env bash
set -euo pipefail

# Usage: build_single_container.sh <flavor> <service>
# Examples:
#   ./build_single_container.sh el9 api
#   ./build_single_container.sh el10 worker

# Get script directory and load configuration functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/container-config.sh
source "${SCRIPT_DIR}/container-config.sh"

if [[ $# -ne 2 ]]; then
    echo "Usage: $0 <flavor> <service>"
    echo "Available flavors:"
    get_available_flavors | sed 's/^/  /'
    echo "Service: api, worker, periodic, etc."
    echo "Examples:"
    echo "  $0 el9 api      # Build API container for EL9"
    echo "  $0 el10 worker  # Build worker container for EL10"
    exit 1
fi

FLAVOR_PARAM="$1"
SERVICE="$2"

# Validate and load flavor configuration
if ! validate_flavor "$FLAVOR_PARAM"; then
    exit 1
fi

if ! load_flavor_config "$FLAVOR_PARAM"; then
    exit 1
fi

# Validate service name
CONTAINER_SERVICES="api pam-issuer worker periodic alert-exporter cli-artifacts userinfo-proxy telemetry-gateway alertmanager-proxy db-setup imagebuilder-api imagebuilder-worker"
if [[ ! " $CONTAINER_SERVICES " =~ " $SERVICE " ]]; then
    echo "Error: Invalid service '$SERVICE'. Must be one of: $CONTAINER_SERVICES"
    exit 1
fi

# Get git information
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
GIT_REF=$(git rev-parse --short HEAD)
SOURCE_GIT_TAG=${SOURCE_GIT_TAG:-$("${ROOT_DIR}"/hack/current-version)}

echo "Building flightctl-${SERVICE} for ${EL_FLAVOR}..."

# Image and version-specific parameters are loaded from container-flavors.conf
# Variables available: EL_FLAVOR, EL_VERSION, BUILD_IMAGE, RUNTIME_IMAGE, MINIMAL_IMAGE, PAM_BASE_URL, PAM_PACKAGE_VERSION
echo "Using configuration: BUILD_IMAGE=${BUILD_IMAGE}"

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
# Add --no-cache for pam-issuer to avoid glibc conflict in cached layers
NO_CACHE_FLAG=""
if [[ "$SERVICE" == "pam-issuer" ]]; then
    NO_CACHE_FLAG="--no-cache"
    echo "Using --no-cache for PAM issuer to avoid glibc conflicts in cached layers"
fi

# shellcheck disable=SC2086
podman build $BUILD_ARGS $NO_CACHE_FLAG \
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