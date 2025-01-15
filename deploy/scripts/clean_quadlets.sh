#!/usr/bin/env bash

source deploy/scripts/env.sh

# Stop services running from the slice or standalone
for service in flightctl.slice 'flightctl-*-standalone.service'; do
    if sudo systemctl is-active --quiet "$service"; then
        echo "Stopping $service..."
        sudo systemctl stop "$service" || echo "Warning: Failed to stop $service"
    fi
done

# Remove copied files
if [ -d "$SYSTEMD_DIR" ]; then
    sudo rm -rf $SYSTEMD_DIR/flightctl* || echo "Warning: Failed to remove quadlet files"
fi

# Remove volumes
for volume in flightctl-db flightctl-api-certs flightctl-redis; do
    if sudo podman volume inspect "$volume" >/dev/null 2>&1; then
        echo "Removing volume $volume"
        sudo podman volume rm "$volume" || echo "Warning: Failed to remove $volume"
    fi
done

# Remove networks
if sudo podman network inspect flightctl >/dev/null 2>&1; then
    echo "Removing network"
    sudo podman network rm flightctl || echo "Warning: Failed to remove network"
fi
