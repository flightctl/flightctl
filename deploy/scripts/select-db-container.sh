#!/usr/bin/env bash

set -eo pipefail

# Database and API container selector script
# This script selects the appropriate container files based on service configuration
# and ensures the correct container files are in place before service startup.

# Configuration paths
CONFIG_WRITEABLE_DIR="${CONFIG_WRITEABLE_DIR:-/etc/flightctl}"
CONFIG_READONLY_DIR="${CONFIG_READONLY_DIR:-/usr/share/flightctl}"
QUADLET_FILES_OUTPUT_DIR="${QUADLET_FILES_OUTPUT_DIR:-/usr/share/containers/systemd}"

# Load init utilities for YAML parsing
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${CONFIG_READONLY_DIR}/init_utils.sh"

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

# Main function to select and install the appropriate database and related service containers
main() {
    echo "Selecting database and database-connected service container configurations..."

    # Ensure output directory exists
    mkdir -p "${QUADLET_FILES_OUTPUT_DIR}"

    local db_target="${QUADLET_FILES_OUTPUT_DIR}/flightctl-db.container"
    local api_target="${QUADLET_FILES_OUTPUT_DIR}/flightctl-api.container"
    local migrate_target="${QUADLET_FILES_OUTPUT_DIR}/flightctl-db-migrate.container"
    local worker_target="${QUADLET_FILES_OUTPUT_DIR}/flightctl-worker.container"
    local periodic_target="${QUADLET_FILES_OUTPUT_DIR}/flightctl-periodic.container"
    local alert_exporter_target="${QUADLET_FILES_OUTPUT_DIR}/flightctl-alert-exporter.container"
    local alertmanager_proxy_target="${QUADLET_FILES_OUTPUT_DIR}/flightctl-alertmanager-proxy.container"

    if is_external_database_enabled; then
        echo "External database enabled - selecting external containers for all database-connected services"

        # Select external database container
        local db_source="${CONFIG_READONLY_DIR}/flightctl-db/flightctl-db-external.container"
        if [[ -f "$db_source" ]]; then
            echo "Installing external database container: $db_source -> $db_target"
            install -m 644 "$db_source" "$db_target"
        else
            echo "Error: External database container file not found: $db_source" >&2
            exit 1
        fi

        # Select external API container
        local api_source="${CONFIG_READONLY_DIR}/flightctl-api/flightctl-api-external.container"
        if [[ -f "$api_source" ]]; then
            echo "Installing external API container: $api_source -> $api_target"
            install -m 644 "$api_source" "$api_target"
        else
            echo "Error: External API container file not found: $api_source" >&2
            exit 1
        fi

        # Select external migration container
        local migrate_source="${CONFIG_READONLY_DIR}/flightctl-db-migrate/flightctl-db-migrate-external.container"
        if [[ -f "$migrate_source" ]]; then
            echo "Installing external migration container: $migrate_source -> $migrate_target"
            install -m 644 "$migrate_source" "$migrate_target"
        else
            echo "Error: External migration container file not found: $migrate_source" >&2
            exit 1
        fi

        # Select external worker container
        local worker_source="${CONFIG_READONLY_DIR}/flightctl-worker/flightctl-worker-external.container"
        if [[ -f "$worker_source" ]]; then
            echo "Installing external worker container: $worker_source -> $worker_target"
            install -m 644 "$worker_source" "$worker_target"
        else
            echo "Error: External worker container file not found: $worker_source" >&2
            exit 1
        fi

        # Select external periodic container
        local periodic_source="${CONFIG_READONLY_DIR}/flightctl-periodic/flightctl-periodic-external.container"
        if [[ -f "$periodic_source" ]]; then
            echo "Installing external periodic container: $periodic_source -> $periodic_target"
            install -m 644 "$periodic_source" "$periodic_target"
        else
            echo "Error: External periodic container file not found: $periodic_source" >&2
            exit 1
        fi

        # Select external alert-exporter container
        local alert_exporter_source="${CONFIG_READONLY_DIR}/flightctl-alert-exporter/flightctl-alert-exporter-external.container"
        if [[ -f "$alert_exporter_source" ]]; then
            echo "Installing external alert-exporter container: $alert_exporter_source -> $alert_exporter_target"
            install -m 644 "$alert_exporter_source" "$alert_exporter_target"
        else
            echo "Error: External alert-exporter container file not found: $alert_exporter_source" >&2
            exit 1
        fi

        # Select external alertmanager-proxy container
        local alertmanager_proxy_source="${CONFIG_READONLY_DIR}/flightctl-alertmanager-proxy/flightctl-alertmanager-proxy-external.container"
        if [[ -f "$alertmanager_proxy_source" ]]; then
            echo "Installing external alertmanager-proxy container: $alertmanager_proxy_source -> $alertmanager_proxy_target"
            install -m 644 "$alertmanager_proxy_source" "$alertmanager_proxy_target"
        else
            echo "Error: External alertmanager-proxy container file not found: $alertmanager_proxy_source" >&2
            exit 1
        fi
    else
        echo "Internal database enabled - selecting standard containers for all database-connected services"

        # Select internal database container
        local db_source="${CONFIG_READONLY_DIR}/flightctl-db/flightctl-db.container"
        if [[ -f "$db_source" ]]; then
            echo "Installing internal database container: $db_source -> $db_target"
            install -m 644 "$db_source" "$db_target"
        else
            echo "Error: Internal database container file not found: $db_source" >&2
            exit 1
        fi

        # Select internal API container
        local api_source="${CONFIG_READONLY_DIR}/flightctl-api/flightctl-api.container"
        if [[ -f "$api_source" ]]; then
            echo "Installing internal API container: $api_source -> $api_target"
            install -m 644 "$api_source" "$api_target"
        else
            echo "Error: Internal API container file not found: $api_source" >&2
            exit 1
        fi

        # Select internal migration container
        local migrate_source="${CONFIG_READONLY_DIR}/flightctl-db-migrate/flightctl-db-migrate.container"
        if [[ -f "$migrate_source" ]]; then
            echo "Installing internal migration container: $migrate_source -> $migrate_target"
            install -m 644 "$migrate_source" "$migrate_target"
        else
            echo "Error: Internal migration container file not found: $migrate_source" >&2
            exit 1
        fi

        # Select internal worker container
        local worker_source="${CONFIG_READONLY_DIR}/flightctl-worker/flightctl-worker.container"
        if [[ -f "$worker_source" ]]; then
            echo "Installing internal worker container: $worker_source -> $worker_target"
            install -m 644 "$worker_source" "$worker_target"
        else
            echo "Error: Internal worker container file not found: $worker_source" >&2
            exit 1
        fi

        # Select internal periodic container
        local periodic_source="${CONFIG_READONLY_DIR}/flightctl-periodic/flightctl-periodic.container"
        if [[ -f "$periodic_source" ]]; then
            echo "Installing internal periodic container: $periodic_source -> $periodic_target"
            install -m 644 "$periodic_source" "$periodic_target"
        else
            echo "Error: Internal periodic container file not found: $periodic_source" >&2
            exit 1
        fi

        # Select internal alert-exporter container
        local alert_exporter_source="${CONFIG_READONLY_DIR}/flightctl-alert-exporter/flightctl-alert-exporter.container"
        if [[ -f "$alert_exporter_source" ]]; then
            echo "Installing internal alert-exporter container: $alert_exporter_source -> $alert_exporter_target"
            install -m 644 "$alert_exporter_source" "$alert_exporter_target"
        else
            echo "Error: Internal alert-exporter container file not found: $alert_exporter_source" >&2
            exit 1
        fi

        # Select internal alertmanager-proxy container
        local alertmanager_proxy_source="${CONFIG_READONLY_DIR}/flightctl-alertmanager-proxy/flightctl-alertmanager-proxy.container"
        if [[ -f "$alertmanager_proxy_source" ]]; then
            echo "Installing internal alertmanager-proxy container: $alertmanager_proxy_source -> $alertmanager_proxy_target"
            install -m 644 "$alertmanager_proxy_source" "$alertmanager_proxy_target"
        else
            echo "Error: Internal alertmanager-proxy container file not found: $alertmanager_proxy_source" >&2
            exit 1
        fi
    fi

    echo "Container selection completed successfully"
}

main "$@"