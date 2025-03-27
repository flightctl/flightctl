#!/usr/bin/env bash

set -eo pipefail

source deploy/scripts/shared.sh
TEMPLATE_DIR="deploy/podman"

deploy_service() {
    local service_name=$1
    local service_full_name="flightctl-${service_name}.service"

    echo "Starting Deployment for $service_full_name"

    # Stop the service if it's running
    systemctl --user stop "$service_full_name" || true

    echo "Performing install for $service_full_name"
    # Handle pre-startup logic for each service
    if [[ "$service_name" == "db" ]]; then
        podman volume rm flightctl-db || true
        podman volume create --opt device=tmpfs --opt type=tmpfs --opt o=nodev,noexec flightctl-db
        ensure_postgres_secrets
    else
        ensure_kv_secrets
    fi

    echo "Moving quadlet files for $service_full_name"
    render_service "$service_name" "standalone"

    start_service "$service_full_name"

    echo "Deployment completed for $service_full_name"
}

if [[ $# -ne 1 ]]; then
    echo "Usage: $0 <service_name>"
    echo "Available services: db, kv"
    exit 1
fi

deploy_service "$1"
