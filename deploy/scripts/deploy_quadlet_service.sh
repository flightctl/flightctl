#!/usr/bin/env bash

set -eo pipefail

source deploy/scripts/env.sh

deploy_service() {
    local service_name=$1
    local service_full_name="flightctl-${service_name}-standalone.service"

    echo "Starting Deployment for $service_full_name"

    # Stop the service if it's running
    sudo systemctl stop $service_full_name || true

    # Handle special handling for the db service
    if [[ "$service_name" == "db" ]]; then
        sudo podman volume rm flightctl-db || true
        sudo podman volume create --opt device=tmpfs --opt type=tmpfs --opt o=nodev,noexec flightctl-db
    fi

    # Copy the configuration files
    sudo mkdir -p "$SYSTEMD_DIR/flightctl-$service_name"
    sudo cp -r deploy/podman/flightctl-$service_name/* "$SYSTEMD_DIR/flightctl-$service_name"

    start_service $service_full_name

    # Handle post-startup logic for db service
    if [[ "$service_name" == "db" ]]; then
        test/scripts/wait_for_postgres.sh podman
        sudo podman exec -it flightctl-db psql -c 'ALTER ROLE admin WITH SUPERUSER'
        sudo podman exec -it flightctl-db createdb admin || true
    fi

    echo "Deployment completed for $service_full_name"
}

if [[ $# -ne 1 ]]; then
    echo "Usage: $0 <service_name>"
    echo "Available services: db, mq, kv"
    exit 1
fi

deploy_service "$1"
