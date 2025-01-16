#!/usr/bin/env bash

export SYSTEMD_DIR="/etc/containers/systemd"

# Reloads systemd config and start the service
start_service() {
    local service_name=$1
    sudo systemctl daemon-reload

    echo "Starting $service_name"
    sudo systemctl start "$service_name"
}
