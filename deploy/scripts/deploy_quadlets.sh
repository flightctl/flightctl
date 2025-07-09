#!/usr/bin/env bash

set -eo pipefail

# Load shared functions first to get the constant directory paths
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh

echo "Starting Deployment"

# Run installation script
if ! deploy/scripts/install.sh; then
    echo "Error: Installation failed"
    exit 1
fi

start_service "flightctl.target"

echo "Checking if all services are running..."

timeout --foreground 300s bash -c '
    expected_services=("flightctl-api" "flightctl-worker" "flightctl-periodic" "flightctl-alert-exporter" "flightctl-db" "flightctl-kv" "flightctl-alertmanager" "flightctl-alertmanager-proxy" "flightctl-cli-artifacts" "flightctl-ui")

    while true; do
        missing_services=()

        for service in "${expected_services[@]}"; do
            if ! podman ps --quiet --filter "name=$service" | grep -q .; then
                missing_services+=("$service")
            fi
        done

        if [ ${#missing_services[@]} -eq 0 ]; then
            echo "All services are running"
            exit 0
        fi

        echo "Waiting for (${#missing_services[@]}/${#expected_services[@]}) services to start: ${missing_services[*]}"
        sleep 10
    done
' || {
    echo "Timeout reached while waiting for services"
    exit 1
}

echo "Deployment completed. Please log in to Flight Control with the following command:"
echo "Login: flightctl login --insecure-skip-tls-verify $(grep baseUrl $CONFIG_WRITEABLE_DIR/flightctl-api/config.yaml | awk '{print $2}')"
echo "Console URL: $(grep baseUIUrl $CONFIG_WRITEABLE_DIR/flightctl-api/config.yaml | awk '{print $2}')"
