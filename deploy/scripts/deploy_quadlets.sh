#!/usr/bin/env bash

set -eo pipefail

source deploy/scripts/shared.sh

echo "Starting Deployment"

PRIMARY_IP=''
if ! PRIMARY_IP=$(bash -c 'source ./test/scripts/functions && get_ext_ip'); then
    echo "Error: Failed to get external IP"
    exit 1
fi
export PRIMARY_IP

BASE_DOMAIN="$PRIMARY_IP.nip.io" TEMPLATE_DIR="deploy/podman" deploy/scripts/installer.sh

start_service "flightctl.slice"

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

echo "Deployment completed. Please log in to Flight Control with the following command:"
echo "Login: flightctl login --insecure-skip-tls-verify $(grep baseUrl $HOME/.config/flightctl/flightctl-api/config.yaml | awk '{print $2}')"
echo "Console URL: $(grep baseUIUrl $HOME/.config/flightctl/flightctl-api/config.yaml | awk '{print $2}')"
