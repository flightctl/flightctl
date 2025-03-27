#!/usr/bin/env bash

: ${BASE_DOMAIN:=""}
: ${AUTH_TYPE:="none"}

# Input and ouput directories
: ${TEMPLATE_DIR:="/etc/flightctl/templates"}
: ${CONFIG_OUTPUT_DIR:="$HOME/.config/flightctl"}
: ${QUADLET_FILES_OUTPUT_DIR:="$HOME/.config/containers/systemd"}

export BASE_DOMAIN CONFIG_OUTPUT_DIR FLIGHTCTL_DISABLE_AUTH TEMPLATE_DIR QUADLET_FILES_OUTPUT_DIR
inject_vars() {
    envsubst '$BASE_DOMAIN $CONFIG_OUTPUT_DIR $FLIGHTCTL_DISABLE_AUTH' < "$1" > "$2"
}

render_service() {
    local service_name="$1"
    local standalone="$2"

    # Determine container template file
    local container_file="${TEMPLATE_DIR}/flightctl-${service_name}/flightctl-${service_name}.container"
    if [[ "$standalone" == "standalone" ]]; then
        container_file="${TEMPLATE_DIR}/flightctl-${service_name}/flightctl-${service_name}-standalone.container"
    fi

    # Process container template
    inject_vars "$container_file" "${QUADLET_FILES_OUTPUT_DIR}/flightctl-${service_name}.container"

    # Ensure config output directory exists
    mkdir -p "${CONFIG_OUTPUT_DIR}/flightctl-${service_name}"

    # Process all files in the config directory
    for config_file in "${TEMPLATE_DIR}/flightctl-${service_name}/flightctl-${service_name}-config"/*; do
        if [[ -f "$config_file" ]]; then
            inject_vars "$config_file" "${CONFIG_OUTPUT_DIR}/flightctl-${service_name}/$(basename "$config_file")"
        fi
    done

    # Move any .volume file if it exists
    for volume in "${TEMPLATE_DIR}/flightctl-${service_name}"/*.volume; do
        if [[ -f "$volume" ]]; then
            cp "$volume" "${QUADLET_FILES_OUTPUT_DIR}"
        fi
    done
}

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
