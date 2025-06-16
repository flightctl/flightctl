#!/usr/bin/env bash

set -eo pipefail

# Directory path for source files
: ${SOURCE_DIR:="deploy"}
: ${IMAGE_TAG:="latest"}

# Load shared functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh

# Export directory paths so they're available to any subprocesses
export CONFIG_READONLY_DIR
export CONFIG_WRITEABLE_DIR
export QUADLET_FILES_OUTPUT_DIR
export SYSTEMD_UNIT_OUTPUT_DIR


# Determine which services should be updated for a given image tag
#
# If image_tag is for a dev build based on the main branch, a matching image tag will not
# exist for the ui container. In this case, fall back to using the latest tag for the ui container.
# Tags for dev builds on the main branch look like: 0.6.0-main-119-gf75bcff
get_services_for_tag() {
    local image_tag="$1"
    local services=()

    if [[ "$image_tag" =~ -main- ]]; then
        services+=("api" "periodic" "worker" "cli-artifacts")
    else
        services+=("api" "periodic" "worker" "ui" "cli-artifacts")
    fi

    echo "${services[@]}"
}

update_image_tags() {
    echo "Updating image tags"
    local image_tag="$1"
    # Check if the image tag is provided
    if [[ -z "$image_tag" ]]; then
        echo "Error: No image tag provided"
        exit 1
    fi
    # Check if the image tag is 'latest'
    if [[ "$image_tag" == "latest" ]]; then
        echo "Using :latest image tag for all containers"
        return
    fi

    # Get the services for this tag
    local services=($(get_services_for_tag "$image_tag"))
    echo "Setting container image tags to: $image_tag for services: ${services[*]}"

    for service in "${services[@]}"; do
        container_file="${QUADLET_FILES_OUTPUT_DIR}/flightctl-${service}.container"
        if [[ -f "$container_file" ]] && grep -q "Image=quay.io/flightctl/" "$container_file"; then
            sed -i "s|Image=quay.io/flightctl/flightctl-${service}:latest|Image=quay.io/flightctl/flightctl-${service}:${image_tag}|" "$container_file"
            echo "Updated $container_file"
        else
            echo "Skipping $container_file (not found or no matching image reference)"
        fi
    done
}

render_files() {
    render_service "api" "${SOURCE_DIR}"
    render_service "periodic" "${SOURCE_DIR}"
    render_service "worker" "${SOURCE_DIR}"
    render_service "alert-exporter" "${SOURCE_DIR}"
    render_service "db" "${SOURCE_DIR}"
    render_service "kv" "${SOURCE_DIR}"
    render_service "alertmanager" "${SOURCE_DIR}"
    render_service "ui" "${SOURCE_DIR}"
    render_service "cli-artifacts" "${SOURCE_DIR}"

    update_image_tags "${IMAGE_TAG}"

    # Create writeable directories for certs and services that generate files
    mkdir -p "${CONFIG_WRITEABLE_DIR}/pki"
    mkdir -p "${CONFIG_WRITEABLE_DIR}/flightctl-api"
    mkdir -p "${CONFIG_WRITEABLE_DIR}/flightctl-ui"
    mkdir -p "${CONFIG_WRITEABLE_DIR}/flightctl-cli-artifacts"

    move_shared_files "${SOURCE_DIR}"
}

main() {
    echo "Starting installation"

    render_files

    echo "Installation complete"
}

main
