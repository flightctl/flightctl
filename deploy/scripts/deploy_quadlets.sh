#!/usr/bin/env bash

set -eo pipefail

source deploy/scripts/env.sh

echo "Starting Deployment"

PRIMARY_IP=''
if ! PRIMARY_IP=$(bash -c 'source ./test/scripts/functions && get_ext_ip'); then
    echo "Error: Failed to get external IP"
    exit 1
fi
export PRIMARY_IP

echo "Copying quadlet unit files"
mkdir -p "$SYSTEMD_DIR"
cp deploy/podman/flightctl.slice "$SYSTEMD_DIR"
cp deploy/podman/flightctl.network "$SYSTEMD_DIR"
find deploy/podman -type f -name "*.container" -exec cp {} "$SYSTEMD_DIR" \;

echo "Copying quadlet config files"
mkdir -p "$CONFIG_DIR"
mkdir -p "$CONFIG_DIR/flightctl-api-config"
touch "$CONFIG_DIR/flightctl-api-config/config.yaml"
envsubst "\$PRIMARY_IP" < deploy/podman/flightctl-api/flightctl-api-config/config.yaml.template > "$CONFIG_DIR/flightctl-api-config/config.yaml"

mkdir -p "$CONFIG_DIR/flightctl-kv-config"
cp deploy/podman/flightctl-kv/flightctl-kv-config/redis.conf "$CONFIG_DIR/flightctl-kv-config/redis.conf"

mkdir -p "$CONFIG_DIR/flightctl-periodic-config"
cp deploy/podman/flightctl-periodic/flightctl-periodic-config/config.yaml "$CONFIG_DIR/flightctl-periodic-config/config.yaml"

mkdir -p "$CONFIG_DIR/flightctl-worker-config"
cp deploy/podman/flightctl-worker/flightctl-worker-config/config.yaml "$CONFIG_DIR/flightctl-worker-config/config.yaml"

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
echo "Login: flightctl login --insecure-skip-tls-verify $(grep baseUrl deploy/podman/flightctl-api/flightctl-api-config/config.yaml | awk '{print $2}')"
echo "Console URL: $(grep baseUIUrl deploy/podman/flightctl-api/flightctl-api-config/config.yaml | awk '{print $2}')"
