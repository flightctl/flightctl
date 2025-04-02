#!/usr/bin/env bash

set -eo pipefail

# Load shared functions first to get the constant directory paths
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh

echo "Starting Deployment"

PRIMARY_IP=''
if ! PRIMARY_IP=$(bash -c 'source ./test/scripts/functions && get_ext_ip'); then
    echo "Error: Failed to get external IP"
    exit 1
fi
export PRIMARY_IP

export BASE_DOMAIN="$PRIMARY_IP.nip.io"
export TEMPLATE_DIR="deploy/podman"

# Run installation script
if ! sudo deploy/scripts/installer.sh; then
    echo "Error: Installation failed"
    exit 1
fi

start_service "flightctl.slice"

echo "Checking if all services are running..."

timeout --foreground 300s bash -c '
    while true; do
        if sudo podman ps --quiet \
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
        sleep 10
    done
' || {
    echo "Timeout reached while waiting for services"
    exit 1
}

echo "Deployment completed. Please log in to Flight Control with the following command:"
echo "Login: flightctl login --insecure-skip-tls-verify $(grep baseUrl $CONFIG_OUTPUT_DIR/flightctl-api/config.yaml | awk '{print $2}')"
echo "Console URL: $(grep baseUIUrl $CONFIG_OUTPUT_DIR/flightctl-api/config.yaml | awk '{print $2}')"
