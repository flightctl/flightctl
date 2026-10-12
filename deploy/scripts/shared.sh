#!/usr/bin/env bash

set -eo pipefail

# Output directory defaults follow the selected manager. User-scope paths can
# be relocated through XDG_*; independent CONFIG_*, BIN_*, and unit-path
# overrides must still match the shared Quadlet specifiers.
if [[ ${EUID} -eq 0 ]]; then
    : "${CONFIG_WRITEABLE_DIR:=/etc/flightctl}"
    : "${CONFIG_READONLY_DIR:=/usr/share/flightctl}"
    : "${QUADLET_FILES_OUTPUT_DIR:=/usr/share/containers/systemd}"
    : "${SYSTEMD_UNIT_OUTPUT_DIR:=/usr/lib/systemd/system}"
    : "${QUADLET_SYSTEMD_DIR:=/etc/containers/systemd}"
    : "${BIN_OUTPUT_DIR:=/usr/bin}"
    : "${VAR_TMP_OUTPUT_DIR:=/var/tmp}"
    : "${VAR_LIB_OUTPUT_DIR:=/var/lib}"
else
    user_home="${HOME:-$(getent passwd "$(id -u)" | cut -d: -f6)}"
    : "${XDG_CONFIG_HOME:=${user_home}/.config}"
    : "${XDG_DATA_HOME:=${user_home}/.local/share}"
    : "${XDG_CACHE_HOME:=${user_home}/.cache}"
    : "${XDG_STATE_HOME:=${user_home}/.local/state}"
    export XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME XDG_STATE_HOME

    : "${CONFIG_WRITEABLE_DIR:=${XDG_CONFIG_HOME}/flightctl}"
    : "${CONFIG_READONLY_DIR:=${XDG_DATA_HOME}/flightctl}"
    : "${QUADLET_FILES_OUTPUT_DIR:=${XDG_CONFIG_HOME}/containers/systemd}"
    : "${SYSTEMD_UNIT_OUTPUT_DIR:=${XDG_CONFIG_HOME}/systemd/user}"
    : "${QUADLET_SYSTEMD_DIR:=${QUADLET_FILES_OUTPUT_DIR}}"
    : "${BIN_OUTPUT_DIR:=${XDG_DATA_HOME}/flightctl/bin}"
    : "${VAR_TMP_OUTPUT_DIR:=${XDG_CACHE_HOME}/flightctl/tmp}"
    : "${VAR_LIB_OUTPUT_DIR:=${XDG_STATE_HOME}}"
fi

# Use this command wrapper for systemd control in deployment helpers. An
# unprivileged process always operates on its own systemd user manager.
run_systemctl() {
    if [[ ${EUID} -eq 0 ]]; then
        systemctl "$@"
    else
        systemctl --user "$@"
    fi
}

require_rootless_local_podman() {
    [[ ${EUID} -eq 0 ]] && return 0

    if [[ -n "${CONTAINER_HOST:-}" || -n "${CONTAINER_CONNECTION:-}" ]]; then
        echo "Error: rootless deployment and cleanup require the local Podman store; unset CONTAINER_HOST and CONTAINER_CONNECTION" >&2
        return 1
    fi

    local podman_context
    podman_context="$(podman info --format '{{.Host.Security.Rootless}} {{.Host.ServiceIsRemote}}' 2>/dev/null || true)"
    if [[ "${podman_context}" != "true false" ]]; then
        echo "Error: unprivileged deployment and cleanup require a local rootless Podman connection" >&2
        return 1
    fi

    return 0
}

validate_runtime_output_paths() {
    local expected_writeable expected_readonly expected_bin expected_quadlet expected_systemd expected_quadlet_systemd

    if [[ ${EUID} -eq 0 ]]; then
        expected_writeable=/etc/flightctl
        expected_readonly=/usr/share/flightctl
        expected_bin=/usr/bin
        expected_systemd=/usr/lib/systemd/system
        expected_quadlet_systemd=/etc/containers/systemd

        case "${QUADLET_FILES_OUTPUT_DIR%/}" in
            /usr/share/containers/systemd|/etc/containers/systemd|/usr/lib/containers/systemd) ;;
            *) echo "Error: QUADLET_FILES_OUTPUT_DIR must be a system Quadlet search path" >&2; return 1 ;;
        esac
        case "${SYSTEMD_UNIT_OUTPUT_DIR%/}" in
            /usr/lib/systemd/system|/etc/systemd/system) ;;
            *) echo "Error: SYSTEMD_UNIT_OUTPUT_DIR must be a system unit path" >&2; return 1 ;;
        esac
        case "${QUADLET_SYSTEMD_DIR%/}" in
            /usr/share/containers/systemd|/etc/containers/systemd|/usr/lib/containers/systemd|/run/containers/systemd) ;;
            *) echo "Error: QUADLET_SYSTEMD_DIR must be a system Quadlet search path" >&2; return 1 ;;
        esac
        if [[ "${VAR_LIB_OUTPUT_DIR%/}" != /var/lib ]]; then
            echo "Error: VAR_LIB_OUTPUT_DIR must be /var/lib in system scope because Grafana and Prometheus use %S" >&2
            return 1
        fi
    else
        local user_home="${HOME:-$(getent passwd "$(id -u)" | cut -d: -f6)}"
        local xdg_config_home="${XDG_CONFIG_HOME:-${user_home}/.config}"
        local xdg_data_home="${XDG_DATA_HOME:-${user_home}/.local/share}"

        if [[ "${xdg_config_home}" != /* || "${xdg_data_home}" != /* || "${XDG_CACHE_HOME}" != /* || "${XDG_STATE_HOME}" != /* ]]; then
            echo "Error: XDG_CONFIG_HOME, XDG_DATA_HOME, XDG_CACHE_HOME, and XDG_STATE_HOME must be absolute paths" >&2
            return 1
        fi

        expected_writeable="${xdg_config_home}/flightctl"
        expected_readonly="${xdg_data_home}/flightctl"
        expected_bin="${xdg_data_home}/flightctl/bin"
        expected_quadlet="${xdg_config_home}/containers/systemd"
        expected_systemd="${xdg_config_home}/systemd/user"
        expected_quadlet_systemd="${expected_quadlet}"
        if [[ "${QUADLET_FILES_OUTPUT_DIR%/}" != "${expected_quadlet%/}" ]]; then
            echo "Error: QUADLET_FILES_OUTPUT_DIR must match the user's Quadlet search path ${expected_quadlet}" >&2
            return 1
        fi
        if [[ "${SYSTEMD_UNIT_OUTPUT_DIR%/}" != "${expected_systemd%/}" ]]; then
            echo "Error: SYSTEMD_UNIT_OUTPUT_DIR must match the user's systemd unit path ${expected_systemd}" >&2
            return 1
        fi
        if [[ "${QUADLET_SYSTEMD_DIR%/}" != "${expected_quadlet_systemd%/}" ]]; then
            echo "Error: QUADLET_SYSTEMD_DIR must match the user's Quadlet search path ${expected_quadlet_systemd}" >&2
            return 1
        fi
        if [[ "${VAR_LIB_OUTPUT_DIR%/}" != "${XDG_STATE_HOME%/}" ]]; then
            echo "Error: VAR_LIB_OUTPUT_DIR must match XDG_STATE_HOME because Grafana and Prometheus use %S" >&2
            return 1
        fi
    fi

    local expected
    for expected in \
        "CONFIG_WRITEABLE_DIR:${expected_writeable}" \
        "CONFIG_READONLY_DIR:${expected_readonly}" \
        "BIN_OUTPUT_DIR:${expected_bin}"; do
        local name="${expected%%:*}"
        local path="${expected#*:}"
        local actual="${!name}"
        if [[ "${actual%/}" != "${path%/}" ]]; then
            echo "Error: ${name} must be ${path}; shared Quadlet units use systemd path specifiers for this directory" >&2
            return 1
        fi
    done

    if [[ "${VAR_TMP_OUTPUT_DIR}" != /* ]]; then
        echo "Error: VAR_TMP_OUTPUT_DIR must be an absolute path" >&2
        return 1
    fi
}

# Load init utilities for YAML parsing
# Handle different ways the script might be sourced
if [[ -n "${BASH_SOURCE[0]}" ]]; then
    SHARED_SCRIPT_DIR="$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")"
else
    SHARED_SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
fi
source "${SHARED_SCRIPT_DIR}"/init_utils.sh

# Check if external database is enabled
is_external_database_enabled() {
    local config_file="${CONFIG_WRITEABLE_DIR}/service-config.yaml"
    if [[ -f "$config_file" ]]; then
        local external_value=$(extract_value "external" "$config_file" | grep -v "^#" | head -1)
        [[ "$external_value" == "enabled" ]]
    else
        false
    fi
}

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
        # Normal mode - process container files based on configuration
        mkdir -p "${QUADLET_FILES_OUTPUT_DIR}"

        # Process container files except standalone ones
        for container_file in "${source_dir}/flightctl-${service_name}"/*.container; do
            if [[ -f "$container_file" ]] &&
               [[ ! "$container_file" == *"-standalone.container" ]]; then
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
    local config_dir="${source_dir}/flightctl-${service_name}/flightctl-${service_name}-config"
    if [[ -d "$config_dir" ]]; then
        for config_file in "$config_dir"/*; do
            if [[ -f "$config_file" ]]; then
                # Ensure config output directory exists
                mkdir -p "${CONFIG_READONLY_DIR}/flightctl-${service_name}"
                local dest_config="${CONFIG_READONLY_DIR}/flightctl-${service_name}/$(basename "$config_file")"
                echo "copy config: ${config_file} -> ${dest_config}"
                cp "$config_file" "${dest_config}"
            fi
        done
    fi

    # Move any .volume file if it exists
    for volume in "${source_dir}/flightctl-${service_name}"/*.volume; do
        if [[ -f "$volume" ]]; then
            local dest_volume="${QUADLET_FILES_OUTPUT_DIR}/$(basename "$volume")"
            echo "copy volume: ${volume} -> ${dest_volume}"
            cp "$volume" "${dest_volume}"
        fi
    done
}


# Start a systemd service
# Args:
#   $1: Service name
start_service() {
    local service_name="$1"
    if [[ ${EUID} -ne 0 ]]; then
        systemctl --user import-environment XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME XDG_STATE_HOME
    fi
    run_systemctl daemon-reload

    echo "Starting service $service_name"
    run_systemctl start "$service_name"
}
