#!/usr/bin/env bash
set -euo pipefail

# Usage: publish_containers.sh <action> <el_version>
# Actions: build, publish
# EL Version: 9, 10

if [[ $# -ne 2 ]]; then
    echo "Usage: $0 <action> <el_version>"
    echo "Actions: build, publish"
    echo "EL Version: 9, 10"
    echo "Examples:"
    echo "  $0 build 9     # Build containers for EL9"
    echo "  $0 publish 10  # Publish containers for EL10"
    exit 1
fi

ACTION="$1"
EL_VERSION="$2"

# Container services to build/publish
CONTAINER_SERVICES="api pam-issuer worker periodic alert-exporter cli-artifacts userinfo-proxy telemetry-gateway alertmanager-proxy db-setup imagebuilder-api imagebuilder-worker"

# Validate inputs
case "$ACTION" in
    build|publish)
        ;;
    *)
        echo "Error: Invalid action '$ACTION'. Must be 'build' or 'publish'"
        exit 1
        ;;
esac

case "$EL_VERSION" in
    9|10)
        ;;
    *)
        echo "Error: Invalid EL version '$EL_VERSION'. Must be '9' or '10'"
        exit 1
        ;;
esac

# Get script directory and source git information
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
GIT_REF=$(git rev-parse --short HEAD)
SOURCE_GIT_TAG=${SOURCE_GIT_TAG:-$(${ROOT_DIR}/hack/current-version)}

case "$ACTION" in
    build)
        echo "Building FlightCtl containers for EL${EL_VERSION}..."

        # Base images are pre-built and pushed to quay.io/flightctl/flightctl-base:el9-* and el10-*
        # No need to build them during container build process

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

        # Build containers using ARG-based containerfiles
        for service in $CONTAINER_SERVICES; do
            echo "Building flightctl-${service}-el${EL_VERSION}..."

            # Determine cache flags
            CACHE_FLAGS=""
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

            podman build $CACHE_FLAGS \
                $BUILD_ARGS \
                --build-arg SOURCE_GIT_TAG="${SOURCE_GIT_TAG}" \
                --build-arg SOURCE_GIT_TREE_STATE="${SOURCE_GIT_TREE_STATE:-clean}" \
                --build-arg SOURCE_GIT_COMMIT="${SOURCE_GIT_COMMIT:-${GIT_REF}}" \
                -f "images/Containerfile.${service}" \
                -t "flightctl-${service}-el${EL_VERSION}:latest" \
                -t "quay.io/flightctl/flightctl-${service}-el${EL_VERSION}:latest" \
                -t "quay.io/flightctl/flightctl-${service}-el${EL_VERSION}:${SOURCE_GIT_TAG}" \
                .
        done

        echo "✓ Built all containers for EL${EL_VERSION}"
        ;;

    publish)
        echo "Publishing FlightCtl containers for EL${EL_VERSION} to registry..."

        for service in $CONTAINER_SERVICES; do
            local_image="flightctl-${service}-el${EL_VERSION}:latest"

            echo "Publishing ${local_image}..."

            # Tag and push to registry with EL version suffix
            podman tag "${local_image}" "quay.io/flightctl/flightctl-${service}-el${EL_VERSION}:latest"
            podman tag "${local_image}" "quay.io/flightctl/flightctl-${service}-el${EL_VERSION}:${GIT_REF}"

            podman push "quay.io/flightctl/flightctl-${service}-el${EL_VERSION}:latest"
            podman push "quay.io/flightctl/flightctl-${service}-el${EL_VERSION}:${GIT_REF}"
        done

        echo "✓ Published all containers for EL${EL_VERSION}"
        echo "Registry images: quay.io/flightctl/flightctl-*-el${EL_VERSION}:latest"
        ;;
esac
