#!/usr/bin/env bash

set -eo pipefail

# Load shared functions first to get the constant directory paths
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
source "${SCRIPT_DIR}"/shared.sh

# Function to switch container images from quay.io to locally built ones for development
switch_to_local_images() {
    echo "Switching container images to locally built ones for development..."
    local services=("api" "worker" "periodic" "alert-exporter" "alertmanager-proxy" "cli-artifacts" "db-migrate" "db-wait" "db-users-init" "pam-issuer")

    for service in "${services[@]}"; do
        container_file="${QUADLET_FILES_OUTPUT_DIR}/flightctl-${service}.container"
        if [[ -f "$container_file" ]] && grep -q "Image=quay.io/flightctl/" "$container_file"; then
            sed -i 's|Image=quay.io/flightctl/flightctl-\([^:][^:]*\):latest|Image=flightctl-\1:latest|' "$container_file"
            echo "Updated $container_file to use local image"
        else
            echo "Skipping $container_file (not found or no matching image reference)"
        fi
    done
}

echo "Starting Deployment"

# Run installation script
if ! deploy/scripts/install.sh; then
    echo "Error: Installation failed"
    exit 1
fi

# Switch to locally built container images for development
switch_to_local_images

echo "Ensuring secrets are available..."
# Always ensure secrets exist before starting services
if ! "${CONFIG_READONLY_DIR}/init_host.sh"; then
    echo "Error: Failed to initialize secrets"
    exit 1
fi

echo "Starting all FlightCtl services via target..."
start_service "flightctl.target"

echo "Waiting for services to initialize..."

# Check if we're using external database
if is_external_database_enabled; then
    echo "External database configured - skipping local database readiness check"
else
    # Wait for database to be ready first
    timeout --foreground 120s bash -c '
        while true; do
            if podman ps --quiet --filter "name=flightctl-db" | grep -q . && \
               podman exec flightctl-db pg_isready -U postgres >/dev/null 2>&1; then
                echo "Database is ready"
                break
            fi
            echo "Waiting for database to become ready..."
            sleep 3
        done
    '
fi

# Wait for database migration to complete
echo "Waiting for database migration to complete..."
timeout --foreground 120s bash -c '
    while true; do
        if systemctl is-active --quiet flightctl-db-migrate.service; then
            echo "Database migration completed"
            break
        fi
        echo "Waiting for database migration to complete..."
        sleep 3
    done
'

# Wait for key-value service
timeout --foreground 60s bash -c '
    while true; do
        if podman ps --quiet --filter "name=flightctl-kv" | grep -q . && \
           podman exec flightctl-kv redis-cli ping >/dev/null 2>&1; then
            echo "Key-value service is ready"
            break
        fi
        echo "Waiting for key-value service..."
        sleep 2
    done
'

echo "Waiting for all services to be fully ready..."
# Get all services from flightctl.target
ALL_SERVICES=$(systemctl show flightctl.target -p Wants --value | tr ' ' '\n' | grep -E '^flightctl-.*\.service$' | sort)

# Wait for core services to be ready
start_time=$(date +%s)
timeout_seconds=120

while true; do
    current_time=$(date +%s)
    elapsed=$((current_time - start_time))

    if [ $elapsed -ge $timeout_seconds ]; then
        echo "Timeout: Core services did not become ready within ${timeout_seconds} seconds"
        exit 1
    fi

    # Check if target is active
    if ! systemctl is-active --quiet flightctl.target; then
        echo "Waiting for flightctl.target to become active..."
        sleep 3
        continue
    fi

    # Check each service
    all_active=true
    for service in ${ALL_SERVICES}; do
        if ! systemctl is-active --quiet "$service"; then
            echo "Waiting for service $service to become active..."
            all_active=false
            break
        fi
    done

    if $all_active; then
        echo "All services are active and ready"
        break
    fi

    sleep 3
done

echo "Deployment completed successfully!"
echo ""
echo "FlightCtl services are running:"
for service in ${ALL_SERVICES}; do
    # Extract a human-readable name from the service name
    service_name=$(echo "$service" | sed 's/flightctl-//g' | sed 's/\.service//g' | sed 's/-/ /g' | sed 's/\b\w/\u&/g')
    if systemctl is-active --quiet "$service"; then
        echo "  ✓ $service_name ($service)"
    else
        echo "  ✗ $service_name ($service) - not active"
    fi
done

echo ""
echo "You can check status with: sudo systemctl status flightctl.target"

