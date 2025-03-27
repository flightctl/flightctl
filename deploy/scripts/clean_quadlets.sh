#!/usr/bin/env bash

source deploy/scripts/shared.sh

# Stop services running from the slice or standalone
for service in flightctl.slice 'flightctl-*-standalone.service'; do
    if systemctl --user is-active --quiet "$service"; then
        echo "Stopping $service..."
        systemctl stop --user "$service" || echo "Warning: Failed to stop $service"
    fi
done

# Remove copied files
if [ -d "$HOME/.config/flightctl" ]; then
    rm -rf "$HOME/.config/flightctl" || echo "Warning: Failed to remove quadlet files"
    rm -rf "$HOME/.config/containers/systemd/flightctl*" || echo "Warning: Failed to remove quadlet config files"
fi

# Remove volumes
for volume in flightctl-db flightctl-api-certs flightctl-kv; do
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

# Remove generated secrets
secrets=("flightctl-postgresql-password" "flightctl-postgresql-master-password" "flightctl-postgresql-user-password" "flightctl-kv-password")
for secret in "${secrets[@]}"; do
    if podman secret inspect "$secret" &>/dev/null; then
        echo "Removing secret $secret"
        podman secret rm "$secret" || echo "Warning: Failed to remove $secret"
    fi
done
