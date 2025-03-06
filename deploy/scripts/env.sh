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
    echo $(cat /dev/urandom | tr -dc 'A-Za-z0-9' | fold -w5 | head -n4 | paste -sd'-')
}

create_secrets() {
    create_postgres_secrets
    create_kv_secrets
}

create_postgres_secrets() {
    if [ -z "${FLIGHTCTL_POSTGRESQL_PASSWORD}" ]; then
        FLIGHTCTL_POSTGRESQL_PASSWORD=$(generate_password)
    fi
    if [ -z "${FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD}" ]; then
        FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD=$(generate_password)
    fi
    if [ -z "${FLIGHTCTL_POSTGRESQL_USER_PASSWORD}" ]; then
        FLIGHTCTL_POSTGRESQL_USER_PASSWORD=$(generate_password)
    fi

    if ! podman secret inspect flightctl-postgresql-password &>/dev/null; then
        echo -n "${FLIGHTCTL_POSTGRESQL_PASSWORD}" | podman secret create flightctl-postgresql-password -
    fi
    if ! podman secret inspect flightctl-postgresql-master-password &>/dev/null; then
        echo -n "${FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD}" | podman secret create flightctl-postgresql-master-password -
    fi
    if ! podman secret inspect flightctl-postgresql-user-password &>/dev/null; then
        echo -n "${FLIGHTCTL_POSTGRESQL_USER_PASSWORD}" | podman secret create flightctl-postgresql-user-password -
    fi
}

create_kv_secrets() {
    if [ -z "${FLIGHTCTL_KV_PASSWORD}" ]; then
        FLIGHTCTL_KV_PASSWORD=$(generate_password)
    fi

    if ! podman secret inspect flightctl-kv-password &>/dev/null; then
        echo -n "${FLIGHTCTL_KV_PASSWORD}" | podman secret create flightctl-kv-password -
    fi
}