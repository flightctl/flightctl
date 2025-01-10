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

envsubst "\$PRIMARY_IP" < deploy/podman/flightctl-api/flightctl-api-config/config.yaml.template > deploy/podman/flightctl-api/flightctl-api-config/config.yaml

echo "Copying all quadlet files"
sudo cp -r deploy/podman/flightctl* $SYSTEMD_DIR

start_service flightctl.slice

echo "Waiting for database to be ready..."
test/scripts/wait_for_postgres.sh podman

echo "Granting superuser privileges to admin role"
sudo podman exec flightctl-db psql -c 'ALTER ROLE admin WITH SUPERUSER'

echo "Checking if all services are running..."

timeout --foreground 300s bash -c '
    while true; do
        if sudo podman ps --quiet \
            --filter "name=flightctl-api" \
            --filter "name=flightctl-worker" \
            --filter "name=flightctl-periodic" \
            --filter "name=flightctl-db" \
            --filter "name=flightctl-rabbitmq" \
            --filter "name=flightctl-kv" \
            --filter "name=flightctl-ui" | wc -l | grep -q 7; then
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
