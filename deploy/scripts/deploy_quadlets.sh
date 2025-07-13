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

echo "Ensuring secrets are available..."
# Always ensure secrets exist before starting services
if ! "${CONFIG_READONLY_DIR}/init_host.sh"; then
    echo "Error: Failed to initialize secrets"
    exit 1
fi

echo "Starting all FlightCtl services via target..."
start_service "flightctl.target"

echo "Waiting for services to initialize..."
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

# Sync database password with secrets
echo "Synchronizing database password with secrets..."
DB_ACTUAL_PASSWORD=$(sudo podman exec flightctl-db printenv POSTGRESQL_MASTER_PASSWORD)
if ! sudo podman run --rm --network flightctl \
    --secret flightctl-postgresql-master-password,type=env,target=DB_PASSWORD \
    quay.io/sclorg/postgresql-16-c9s:latest \
    bash -c 'PGPASSWORD="$DB_PASSWORD" psql -h flightctl-db -U admin -d flightctl -c "SELECT 1" >/dev/null 2>&1'; then
    
    echo "Password mismatch detected! Fixing secret..."
    sudo podman secret rm flightctl-postgresql-master-password
    echo "$DB_ACTUAL_PASSWORD" | sudo podman secret create flightctl-postgresql-master-password -
    echo "Secret updated to match database password"
fi

# Ensure admin has superuser privileges
echo "Ensuring database admin has superuser privileges..."
if sudo podman exec flightctl-db psql -U postgres -tAc "SELECT rolsuper FROM pg_roles WHERE rolname = 'admin'" | grep -q "f"; then
    echo "Granting superuser privileges to admin user..."
    sudo podman exec flightctl-db psql -U postgres -c "ALTER USER admin WITH SUPERUSER;"
fi

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

# Restart any failed services due to initial password/permission issues
echo "Restarting API services to apply fixes..."
sudo systemctl restart flightctl-api.service flightctl-worker.service flightctl-periodic.service

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

