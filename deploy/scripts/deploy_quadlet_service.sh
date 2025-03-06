#!/usr/bin/env bash

set -eo pipefail

source deploy/scripts/env.sh

deploy_service() {
    local service_name=$1
    local service_full_name="flightctl-${service_name}-standalone.service"

    echo "Starting Deployment for $service_full_name"

    # Stop the service if it's running
    systemctl --user stop "$service_full_name" || true

    # Handle pre-startup logic for each service
    if [[ "$service_name" == "db" ]]; then
        podman volume rm flightctl-db || true
        podman volume create --opt device=tmpfs --opt type=tmpfs --opt o=nodev,noexec flightctl-db
        create_postgres_secrets
    else
        # Copy configuration files
        mkdir -p "$CONFIG_DIR/flightctl-$service_name-config"
        cp deploy/podman/flightctl-kv/flightctl-kv-config/redis.conf "$CONFIG_DIR/flightctl-kv-config/redis.conf"
        create_kv_secrets
    fi

    mkdir -p "$SYSTEMD_DIR"
    cp deploy/podman/flightctl-$service_name/flightctl-$service_name-standalone.container "$SYSTEMD_DIR"
    cp deploy/podman/flightctl.network "$SYSTEMD_DIR"

    start_service $service_full_name

    # Handle post-startup logic for db service
    if [[ "$service_name" == "db" ]]; then
        test/scripts/wait_for_postgres.sh podman
        podman exec flightctl-db psql -c 'ALTER ROLE admin WITH SUPERUSER'
        podman exec flightctl-db createdb admin || true
    fi

    echo "Deployment completed for $service_full_name"
}

if [[ $# -ne 1 ]]; then
    echo "Usage: $0 <service_name>"
    echo "Available services: db, kv"
    exit 1
fi

deploy_service "$1"
