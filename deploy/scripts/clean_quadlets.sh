#!/usr/bin/env bash

set -eo pipefail

# Load shared functions which contain the read-only directory constants
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh

clean_services() {
    # Stop any running services
    for service in flightctl.target flightctl-*.service; do
        if  systemctl is-active --quiet "$service"; then
            echo "Stopping $service..."
            systemctl stop "$service" || echo "Warning: Failed to stop service $service"
        fi
    done
}

clean_files() {
    # Use the read-only directory constants from shared.sh
    echo "Removing read-only configuration files from ${CONFIG_READONLY_DIR}"
    rm -rf "$CONFIG_READONLY_DIR" || echo "Warning: Failed to remove read-only config files"

    echo "Removing writeable configuration files from ${CONFIG_WRITEABLE_DIR}"
    rm -rf "$CONFIG_WRITEABLE_DIR" || echo "Warning: Failed to remove writeable config files"

    echo "Removing quadlet files from ${QUADLET_FILES_OUTPUT_DIR}"
    rm -rf "$QUADLET_FILES_OUTPUT_DIR/flightctl"* || echo "Warning: Failed to remove quadlet config files"

    echo "Removing systemd unit files from ${SYSTEMD_UNIT_OUTPUT_DIR}"
    rm -rf "$SYSTEMD_UNIT_OUTPUT_DIR/flightctl"* || echo "Warning: Failed to remove systemd unit files"
}

clean_volumes() {
    # Remove volumes
    for volume in flightctl-db flightctl-api-certs flightctl-kv flightctl-ui-certs flightctl-cli-artifacts-certs flightctl-alertmanager flightctl-alertmanager-proxy flightctl-alert-exporter; do
        if podman volume inspect "$volume" >/dev/null 2>&1; then
            echo "Removing volume $volume"
            podman volume rm "$volume" || echo "Warning: Failed to remove volume $volume"
        fi
    done
}

clean_networks() {
    # Remove networks
    if podman network inspect flightctl >/dev/null 2>&1; then
        echo "Removing network"
        podman network rm flightctl || echo "Warning: Failed to remove network"
    fi
}

clean_secrets() {
    # Remove generated secrets
    secrets=("flightctl-postgresql-password" "flightctl-postgresql-master-password" "flightctl-postgresql-user-password" "flightctl-kv-password" "flightctl-alertmanager-password" "flightctl-alertmanager-proxy-password")
    for secret in "${secrets[@]}"; do
        if  podman secret inspect "$secret" &>/dev/null; then
            echo "Removing secret $secret"
            podman secret rm "$secret" || echo "Warning: Failed to remove secret $secret"
        fi
    done
}

main() {
    echo "Starting cleanup"

    clean_services
    clean_files
    clean_volumes
    clean_networks
    clean_secrets

    echo "Cleanup completed"
}

main
