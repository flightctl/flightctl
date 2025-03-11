#!/usr/bin/env bash

export SYSTEMD_DIR="$HOME/.config/containers/systemd"
export CONFIG_DIR="$HOME/.config/flightctl"

# Reloads systemd config and start the service
start_service() {
    local service_name=$1
    systemctl --user daemon-reload

    echo "Starting $service_name"
    systemctl --user start "$service_name"
}
