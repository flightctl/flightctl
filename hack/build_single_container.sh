#!/usr/bin/env bash
set -euo pipefail

# Usage: build_single_container.sh <flavor> <service>
# Examples:
#   ./build_single_container.sh el9 api
#   ./build_single_container.sh el10 worker

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
: "${FLAVORCTL:=${ROOT_DIR}/bin/flavorctl}"

if [[ $# -ne 2 ]]; then
    echo "Usage: $0 <flavor> <service>"
    echo "Available flavors:"
    ${FLAVORCTL} list | sed 's/^/  /'
    echo "Service: api, worker, periodic, etc."
    exit 1
fi

FLAVOR_PARAM="$1"
SERVICE="$2"

eval "$(${FLAVORCTL} export-build "$FLAVOR_PARAM")"

# Validate service name
CONTAINER_SERVICES="api pam-issuer worker periodic alert-exporter cli-artifacts userinfo-proxy telemetry-gateway alertmanager-proxy db-setup imagebuilder-api imagebuilder-worker"
if [[ ! " $CONTAINER_SERVICES " =~ " $SERVICE " ]]; then
    echo "Error: Invalid service '$SERVICE'. Must be one of: $CONTAINER_SERVICES"
    exit 1
fi

GIT_REF=$(git rev-parse --short HEAD)
SOURCE_GIT_TAG=${SOURCE_GIT_TAG:-$("${ROOT_DIR}"/hack/current-version)}

echo "Building flightctl-${SERVICE} for ${EL_FLAVOR}..."
echo "Using configuration: BUILD_IMAGE=${BUILD_IMAGE}"

# Determine runtime image based on service type
if [[ "$SERVICE" == "cli-artifacts" || "$SERVICE" == "db-setup" ]]; then
    SERVICE_RUNTIME_IMAGE="$PACKAGE_MINIMAL_IMAGE"
else
    SERVICE_RUNTIME_IMAGE="$RUNTIME_IMAGE"
fi

BUILD_ARGS="--build-arg BUILD_IMAGE=${BUILD_IMAGE} --build-arg RUNTIME_IMAGE=${SERVICE_RUNTIME_IMAGE} --build-arg EL_VERSION=${EL_VERSION}"

if [[ "$SERVICE" == "pam-issuer" ]]; then
    BUILD_ARGS="$BUILD_ARGS --build-arg PAM_BASE_URL=${PAM_BASE_URL} --build-arg PAM_PACKAGE_VERSION=${PAM_PACKAGE_VERSION}"
fi

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

echo "Built flightctl-${SERVICE} for ${EL_FLAVOR}"
echo "Local image: flightctl-${SERVICE}:${EL_FLAVOR}-latest"
