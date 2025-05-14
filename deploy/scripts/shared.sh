#!/usr/bin/env bash

set -eo pipefail

# Output directory paths - allow overrides via environment variables
: ${CONFIG_WRITEABLE_DIR:="/etc/flightctl"}
: ${CONFIG_READONLY_DIR:="/usr/share/flightctl"}
: ${QUADLET_FILES_OUTPUT_DIR:="/usr/share/containers/systemd"}
: ${SYSTEMD_UNIT_OUTPUT_DIR:="/usr/lib/systemd/system"}

# Render a service configuration
# Args:
#   $1: Service name
#   $2: Source directory path
#   $3: "standalone" if using standalone mode (optional)
render_service() {
    local service_name="$1"
    local source_dir="$2/podman"
    local standalone="$3"

    # Process container files
    if [[ "$standalone" == "standalone" ]]; then
        # Standalone mode - use only the standalone container file
        local container_file="${source_dir}/flightctl-${service_name}/flightctl-${service_name}-standalone.container"

        # Ensure quadlet output directory exists
        mkdir -p "${QUADLET_FILES_OUTPUT_DIR}"

        # Process standalone container file
        cp "$container_file" "${QUADLET_FILES_OUTPUT_DIR}/flightctl-${service_name}.container"
    else
        # Normal mode - process all container files except standalone ones
        mkdir -p "${QUADLET_FILES_OUTPUT_DIR}"

        for container_file in "${source_dir}/flightctl-${service_name}"/*.container; do
            if [[ -f "$container_file" ]] && [[ ! "$container_file" == *"-standalone.container" ]]; then
                local base_filename=$(basename "$container_file")
                cp "$container_file" "${QUADLET_FILES_OUTPUT_DIR}/${base_filename}"
            fi
        done
    fi

    # Process all files in the config directory
    for config_file in "${source_dir}/flightctl-${service_name}/flightctl-${service_name}-config"/*; do
        if [[ -f "$config_file" ]]; then
            # Ensure config output directory exists
            mkdir -p "${CONFIG_READONLY_DIR}/flightctl-${service_name}"
            cp "$config_file" "${CONFIG_READONLY_DIR}/flightctl-${service_name}/$(basename "$config_file")"
        fi
    done

    # Move any .volume file if it exists
    for volume in "${source_dir}/flightctl-${service_name}"/*.volume; do
        if [[ -f "$volume" ]]; then
            cp "$volume" "${QUADLET_FILES_OUTPUT_DIR}"
        fi
    done
}

move_shared_files() {
    local source_dir="$1"
    # Copy the network and target files
    cp "${source_dir}/podman/flightctl.network" "${QUADLET_FILES_OUTPUT_DIR}"

    mkdir -p "${SYSTEMD_UNIT_OUTPUT_DIR}"
    cp "${source_dir}/podman/flightctl.target" "${SYSTEMD_UNIT_OUTPUT_DIR}"

    # Copy writeable files
    cp "${source_dir}/podman/service-config.yaml" "${CONFIG_WRITEABLE_DIR}/service-config.yaml"

    # Copy read only files
    cp "${source_dir}/scripts/init_utils.sh" "${CONFIG_READONLY_DIR}/init_utils.sh"
    cp "${source_dir}/scripts/init_utils.sh" "${CONFIG_READONLY_DIR}/init_utils_host.sh"
    cp "${source_dir}/scripts/init_host.sh" "${CONFIG_READONLY_DIR}/init_host.sh"
    cp "${source_dir}/scripts/secrets.sh" "${CONFIG_READONLY_DIR}/secrets.sh"
}

# Start a systemd service
# Args:
#   $1: Service name
start_service() {
    local service_name="$1"
    systemctl daemon-reload

    echo "Starting service $service_name"
    systemctl start "$service_name"
}
