#!/usr/bin/env bash

source deploy/scripts/env.sh

# Stop services running from the slice or standalone
for service in flightctl.slice 'flightctl-*-standalone.service'; do
    if systemctl --user is-active --quiet "$service"; then
        echo "Stopping $service..."
        systemctl stop --user "$service" || echo "Warning: Failed to stop $service"
    fi
done

# Remove copied files
if [ -d "$SYSTEMD_DIR" ]; then
    rm -rf $SYSTEMD_DIR/flightctl* || echo "Warning: Failed to remove quadlet files"
    rm -rf $CONFIG_DIR/flightctl-* || echo "Warning: Failed to remove quadlet config files"
fi

# Remove volumes
for volume in flightctl-db flightctl-api-certs flightctl-redis; do
    if podman volume inspect "$volume" >/dev/null 2>&1; then
        echo "Removing volume $volume"
        podman volume rm "$volume" || echo "Warning: Failed to remove $volume"
    fi
done

# Remove networks
if podman network inspect flightctl >/dev/null 2>&1; then
    echo "Removing network"
    podman network rm flightctl || echo "Warning: Failed to remove network"
fi
