#!/usr/bin/env bash

set -eo pipefail

source deploy/scripts/env.sh

deploy_services() {
    local template_dir=${1:-$TEMPLATE_DIR}
    local config_dir=${2:-$CONFIG_DIR}

    echo "Starting Deployment"

    PRIMARY_IP=''
    if ! PRIMARY_IP=$(bash -c 'source ./test/scripts/functions && get_ext_ip'); then
        echo "Error: Failed to get external IP"
        exit 1
    fi
    export PRIMARY_IP

    echo "Copying quadlet unit files"
    mkdir -p "$SYSTEMD_DIR"
    cp "$template_dir/flightctl.slice" "$SYSTEMD_DIR"
    cp "$template_dir/flightctl.network" "$SYSTEMD_DIR"
    find "$template_dir" -type f -name "*.container" -exec cp {} "$SYSTEMD_DIR" \;

    echo "Copying quadlet config files"
    mkdir -p "$config_dir"
    mkdir -p "$config_dir/flightctl-api-config"
    touch "$config_dir/flightctl-api-config/config.yaml"
    envsubst "\$PRIMARY_IP" < "$template_dir/flightctl-api/flightctl-api-config/config.yaml.template" > "$config_dir/flightctl-api-config/config.yaml"

    mkdir -p "$CONFIG_DIR/flightctl-kv-config"
    cp "$template_dir/flightctl-kv/flightctl-kv-config/redis.conf" "$config_dir/flightctl-kv-config/redis.conf"

    mkdir -p "$CONFIG_DIR/flightctl-periodic-config"
    cp "$template_dir/flightctl-periodic/flightctl-periodic-config/config.yaml" "$config_dir/flightctl-periodic-config/config.yaml"

    mkdir -p "$CONFIG_DIR/flightctl-worker-config"
    cp "$template_dir/flightctl-worker/flightctl-worker-config/config.yaml" "$config_dir/flightctl-worker-config/config.yaml"

    start_service flightctl.slice

    echo "Waiting for database to be ready..."
    test/scripts/wait_for_postgres.sh podman

    echo "Granting superuser privileges to admin role"
    podman exec flightctl-db psql -c 'ALTER ROLE admin WITH SUPERUSER'

    echo "Checking if all services are running..."

    timeout --foreground 300s bash -c '
        while true; do
            if podman ps --quiet \
                --filter "name=flightctl-api" \
                --filter "name=flightctl-worker" \
                --filter "name=flightctl-periodic" \
                --filter "name=flightctl-db" \
                --filter "name=flightctl-kv" \
                --filter "name=flightctl-ui" | wc -l | grep -q 6; then
                echo "All services are running"
                exit 0
            fi
            echo "Waiting for all services to be running..."
            sleep 5
        done
    ' || {
        echo "Timeout reached while waiting for services"
        exit 1
    }

    echo "Deployment completed. Please, login to FlightCtl with the following command:"
    echo "Login: flightctl login --insecure-skip-tls-verify $(grep baseUrl "$config_dir/flightctl-api-config/config.yaml" | awk '{print $2}')"
    echo "Console URL: $(grep baseUIUrl "$config_dir/flightctl-api-config/config.yaml" | awk '{print $2}')"
}

deploy_services "$1" "$2"
