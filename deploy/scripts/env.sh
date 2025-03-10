#!/usr/bin/env bash

export SYSTEMD_DIR="$HOME/.config/containers/systemd"
export CONFIG_DIR="$HOME/.config/flightctl"

postgres_secrets=("flightctl-postgresql-password" "flightctl-postgresql-master-password" "flightctl-postgresql-user-password")
kv_secrets=("flightctl-kv-password")
export SECRETS=("${postgres_secrets[@]}" "${kv_secrets[@]}")

# Reloads systemd config and start the service
start_service() {
    local service_name=$1
    systemctl --user daemon-reload

    echo "Starting $service_name"
    systemctl --user start "$service_name"
}

generate_password() {
    echo "$(cat /dev/urandom | tr -dc 'A-Za-z0-9' | fold -w5 | head -n4 | paste -sd'-')"
}

ensure_secrets() {
    ensure_postgres_secrets
    ensure_kv_secrets
}

ensure_postgres_secrets() {
    echo "Ensuring secrets for PostgreSQL"
    if [ -z "${FLIGHTCTL_POSTGRESQL_PASSWORD}" ]; then
        export FLIGHTCTL_POSTGRESQL_PASSWORD=$(generate_password)
    fi
    if [ -z "${FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD}" ]; then
        export FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD=$(generate_password)
    fi
    if [ -z "${FLIGHTCTL_POSTGRESQL_USER_PASSWORD}" ]; then
        export FLIGHTCTL_POSTGRESQL_USER_PASSWORD=$(generate_password)
    fi

    if ! podman secret exists flightctl-postgresql-password; then
        echo "Creating secret flightctl-postgresql-password"
        if ! podman secret create --env flightctl-postgresql-password FLIGHTCTL_POSTGRESQL_PASSWORD; then
            echo "Error creating secret flightctl-postgresql-password"
        fi
    fi
    if ! podman secret exists flightctl-postgresql-master-password; then
        echo "Creating secret flightctl-postgresql-master-password"
        if ! podman secret create --env flightctl-postgresql-master-password FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD; then
            echo "Error creating secret flightctl-postgresql-master-password"
        fi
    fi
    if ! podman secret exists flightctl-postgresql-user-password; then
        echo "Creating secret flightctl-postgresql-user-password"
        if ! podman secret create --env flightctl-postgresql-user-password FLIGHTCTL_POSTGRESQL_USER_PASSWORD; then
            echo "Error creating secret flightctl-postgresql-user-password"
        fi
    fi
}

ensure_kv_secrets() {
    echo "Ensuring secrets for KV"
    if [ -z "${FLIGHTCTL_KV_PASSWORD}" ]; then
        export FLIGHTCTL_KV_PASSWORD=$(generate_password)
    fi

    if ! podman secret exists flightctl-kv-password; then
        echo "Creating secret flightctl-kv-password"
        if ! podman secret create --env flightctl-kv-password FLIGHTCTL_KV_PASSWORD; then
            echo "Error creating secret flightctl-kv-password"
        fi
    fi
}