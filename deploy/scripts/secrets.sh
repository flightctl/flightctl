#!/usr/bin/env bash

# Generate a random password
# Returns: A random password string
generate_password() {
    echo "$(dd bs=512 if=/dev/urandom count=1 2>/dev/null | LC_ALL=C tr -dc 'A-Za-z0-9' | fold -w5 | head -n4 | paste -sd '-')"
}

# Ensure all required secrets exist
ensure_secrets() {
    ensure_postgres_secrets
    ensure_kv_secrets
}

# Ensure PostgreSQL secrets exist
ensure_postgres_secrets() {
    echo "Ensuring secrets for PostgreSQL"
    ensure_secret "flightctl-postgresql-password" "FLIGHTCTL_POSTGRESQL_PASSWORD"
    ensure_secret "flightctl-postgresql-master-password" "FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD"
    ensure_secret "flightctl-postgresql-user-password" "FLIGHTCTL_POSTGRESQL_USER_PASSWORD"
}

# Ensure KV secrets exist
ensure_kv_secrets() {
    echo "Ensuring secrets for KV"
    ensure_secret "flightctl-kv-password" "FLIGHTCTL_KV_PASSWORD"
}

# Ensure a specific secret exists
# Args:
#   $1: Secret name
#   $2: Environment variable name to store the secret
ensure_secret() {
    local secret_name="$1"
    local env_var_name="$2"

    if ! sudo podman secret exists "$secret_name"; then
        echo "Creating secret $secret_name"
        if [ -z "${!env_var_name}" ]; then
            echo "Generating password for $env_var_name"
            export "$env_var_name"="$(generate_password)"
        else
            echo "Using existing environment variable $env_var_name"
        fi
        if ! sudo -E podman secret create --env "$secret_name" "$env_var_name"; then
            echo "Error creating secret $secret_name"
            return 1
        fi
    fi
    return 0
}
