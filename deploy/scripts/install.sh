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

update_image_tags() {
    local image_tag="$1"
    # Check if the image tag is provided
    if [[ -z "$image_tag" ]]; then
        echo "Error: No image tag provided"
        exit 1
    fi
    # Check if the image tag is latest - this is the default tag so no need to write
    if [[ "$image_tag" == "latest" ]]; then
        echo "Using :latest image tag"
        return
    fi

    echo "Setting container image tags to: $image_tag"

    # Find all container files for flightctl services and update image tags
    find "${QUADLET_FILES_OUTPUT_DIR}" -name "flightctl-*.container" | while read -r container_file; do
        if grep -q "Image=quay.io/flightctl/" "$container_file"; then
            sed -i "s|Image=quay.io/flightctl/\([^:]*\):latest|Image=quay.io/flightctl/\1:${image_tag}|" "$container_file"
            echo "Updated $container_file"
        fi
    done
}

render_files() {
    render_service "api" "${SOURCE_DIR}"
    render_service "periodic" "${SOURCE_DIR}"
    render_service "worker" "${SOURCE_DIR}"
    render_service "db" "${SOURCE_DIR}"
    render_service "kv" "${SOURCE_DIR}"
    render_service "ui" "${SOURCE_DIR}"

    update_image_tags "${IMAGE_TAG}"

    # Create writeable directories for certs and services that generate files
    mkdir -p "${CONFIG_WRITEABLE_DIR}/pki"
    mkdir -p "${CONFIG_WRITEABLE_DIR}/flightctl-api"
    mkdir -p "${CONFIG_WRITEABLE_DIR}/flightctl-ui"

    move_shared_files "${SOURCE_DIR}"
}

main() {
    echo "Starting installation"

    render_files

    echo "Installation complete"
}

main
