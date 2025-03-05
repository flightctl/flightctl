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
    echo "$(dd bs=512 if=/dev/urandom count=1 2>/dev/null | LC_ALL=C tr -dc 'A-Za-z0-9' | fold -w5 | head -n4 | paste -sd '-')"
}

ensure_secrets() {
    ensure_postgres_secrets
    ensure_kv_secrets
}

ensure_postgres_secrets() {
    echo "Ensuring secrets for PostgreSQL"
    ensure_secret "flightctl-postgresql-password" "FLIGHTCTL_POSTGRESQL_PASSWORD"
    ensure_secret "flightctl-postgresql-master-password" "FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD"
    ensure_secret "flightctl-postgresql-user-password" "FLIGHTCTL_POSTGRESQL_USER_PASSWORD"
}

ensure_kv_secrets() {
    echo "Ensuring secrets for KV"
    ensure_secret "flightctl-kv-password" "FLIGHTCTL_KV_PASSWORD"
}

ensure_secret() {
    local secret_name=$1
    local env_var_name=$2

    if ! podman secret exists "$secret_name"; then
        echo "Creating secret $secret_name"
        if [ -z "${!env_var_name}" ]; then
            echo "Generating password for $env_var_name"
            export "$env_var_name"="$(generate_password)"
        else
            echo "Using existing environment variable $env_var_name"
        fi
        if ! podman secret create --env "$secret_name" "$env_var_name"; then
            echo "Error creating secret $secret_name"
        fi
    fi
}
