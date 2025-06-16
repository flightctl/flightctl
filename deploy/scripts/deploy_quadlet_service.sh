#!/usr/bin/env bash

set -eo pipefail

# Directory path for source files
SOURCE_DIR="deploy"

# Load shared functions
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh
source "${SCRIPT_DIR}"/secrets.sh

deploy_service() {
    local service_name=$1
    local service_full_name="flightctl-${service_name}.service"

    echo "Starting Deployment for $service_full_name"

    # Stop the service if it's running
    systemctl stop "$service_full_name" || true

    echo "Performing install for $service_full_name"
    # Handle pre-startup logic for each service
    if [[ "$service_name" == "db" ]]; then
        podman volume rm flightctl-db || true
        podman volume create --opt device=tmpfs --opt type=tmpfs --opt o=nodev,noexec flightctl-db
        ensure_postgres_secrets
    elif [[ "$service_name" == "kv" ]]; then
        ensure_kv_secrets
    else
        echo "No pre-startup logic for $service_name"
    fi

    echo "Installing quadlet files for $service_full_name"

    render_service "$service_name" "${SOURCE_DIR}" "standalone"
    start_service "$service_full_name"

    echo "Deployment completed for $service_full_name"
}

main() {
    if [[ $# -ne 1 ]]; then
        echo "Usage: $0 <service_name>"
        echo "Available services: db, kv, alertmanager"
        exit 1
    fi

    # Validate service name
    local service_name="$1"
    if [[ ! "$service_name" =~ ^(db|kv|alertmanager)$ ]]; then
        echo "Error: Invalid service name: $service_name"
        echo "Available services: db, kv, alertmanager"
        exit 1
    fi

    deploy_service "$service_name"
}

# Execute the main function with all command line arguments
main "$@"
