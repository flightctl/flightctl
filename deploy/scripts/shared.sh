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
        local container_file="${source_dir}/flightctl-${service_name}/flightctl-${service_name}-standalone.container"

        # Ensure quadlet output directory exists
        mkdir -p "${QUADLET_FILES_OUTPUT_DIR}"

        # Process standalone container file
        local dest_container="${QUADLET_FILES_OUTPUT_DIR}/flightctl-${service_name}.container"

        # Validate source file exists and is readable
        if [[ ! -f "$container_file" ]]; then
            echo "Error: Source container file does not exist: $container_file" >&2
            exit 1
        fi
        if [[ ! -r "$container_file" ]]; then
            echo "Error: Source container file is not readable: $container_file" >&2
            exit 1
        fi

        echo "copy container: ${container_file} -> ${dest_container}"
        install -m 644 "$container_file" "${dest_container}"
    else
        # Normal mode - process all container files except standalone ones
        mkdir -p "${QUADLET_FILES_OUTPUT_DIR}"

        for container_file in "${source_dir}/flightctl-${service_name}"/*.container; do
            if [[ -f "$container_file" ]] && [[ ! "$container_file" == *"-standalone.container" ]]; then
                local base_filename=$(basename "$container_file")
                local dest_container="${QUADLET_FILES_OUTPUT_DIR}/${base_filename}"

                # Validate source file exists and is readable
                if [[ ! -r "$container_file" ]]; then
                    echo "Error: Source container file is not readable: $container_file" >&2
                    exit 1
                fi

                echo "copy container: ${container_file} -> ${dest_container}"
                install -m 644 "$container_file" "${dest_container}"
            fi
        done

        # Process .service files for systemd services
        for service_file in "${source_dir}/flightctl-${service_name}"/*.service; do
            if [[ -f "$service_file" ]]; then
                local base_filename=$(basename "$service_file")
                # Guarantee target dir exists to avoid a fatal cp error
                mkdir -p "${SYSTEMD_UNIT_OUTPUT_DIR}"
                local dest_service="${SYSTEMD_UNIT_OUTPUT_DIR}/${base_filename}"
                echo "copy service: ${service_file} -> ${dest_service}"
                cp "$service_file" "${dest_service}"
            fi
        done
    fi

    # Process all files in the config directory
    for config_file in "${source_dir}/flightctl-${service_name}/flightctl-${service_name}-config"/*; do
        if [[ -f "$config_file" ]]; then
            # Ensure config output directory exists
            mkdir -p "${CONFIG_READONLY_DIR}/flightctl-${service_name}"
            local dest_config="${CONFIG_READONLY_DIR}/flightctl-${service_name}/$(basename "$config_file")"
            echo "copy config: ${config_file} -> ${dest_config}"
            cp "$config_file" "${dest_config}"
        fi
    done

    # Move any .volume file if it exists
    for volume in "${source_dir}/flightctl-${service_name}"/*.volume; do
        if [[ -f "$volume" ]]; then
            local dest_volume="${QUADLET_FILES_OUTPUT_DIR}/$(basename "$volume")"
            echo "copy volume: ${volume} -> ${dest_volume}"
            cp "$volume" "${dest_volume}"
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
    cp "${source_dir}/scripts/init_host.sh" "${CONFIG_READONLY_DIR}/init_host.sh"
    cp "${source_dir}/scripts/secrets.sh" "${CONFIG_READONLY_DIR}/secrets.sh"

    # Copy migration helper scripts for db-migrate service
    mkdir -p "${CONFIG_READONLY_DIR}/flightctl-db-migrate"
    cp "${source_dir}/scripts/migration-setup.sh" "${CONFIG_READONLY_DIR}/flightctl-db-migrate/migration-setup.sh"
    cp "${source_dir}/scripts/wait-for-database.sh" "${CONFIG_READONLY_DIR}/flightctl-db-migrate/wait-for-database.sh"
    chmod +x "${CONFIG_READONLY_DIR}/flightctl-db-migrate/migration-setup.sh"
    chmod +x "${CONFIG_READONLY_DIR}/flightctl-db-migrate/wait-for-database.sh"
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
