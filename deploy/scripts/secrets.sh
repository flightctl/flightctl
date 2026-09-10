#!/usr/bin/env bash

set -eo pipefail

# Generate a random password
# Returns: A random password string
generate_password() {
    echo "$(dd bs=512 if=/dev/urandom count=1 2>/dev/null | LC_ALL=C tr -dc 'A-Za-z0-9' | fold -w5 | head -n4 | paste -sd '-')"
}

# Ensure PostgreSQL secrets exist
ensure_postgres_secrets() {
    echo "Ensuring secrets for PostgreSQL"

    # Use Podman secrets consistently for both internal and external databases
    # For external databases, users should create the secrets manually
    # For internal databases, secrets will be auto-generated if they don't exist

    ensure_secret "flightctl-postgresql-password" "FLIGHTCTL_POSTGRESQL_PASSWORD"
    ensure_secret "flightctl-postgresql-master-password" "FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD"
    ensure_secret "flightctl-postgresql-user-password" "FLIGHTCTL_POSTGRESQL_USER_PASSWORD"
    ensure_secret "flightctl-postgresql-migrator-password" "FLIGHTCTL_POSTGRESQL_MIGRATOR_PASSWORD"
}

# Ensure KV secrets exist
ensure_kv_secrets() {
    echo "Ensuring secrets for KV"
    ensure_secret "flightctl-kv-password" "FLIGHTCTL_KV_PASSWORD"
}

ensure_delta_generation_secrets() {
    echo "Ensuring secrets for delta generation default repository"
    ensure_env_secret "flightctl-delta-generation-default-repository-username" "DELTA_GENERATION_DEFAULT_REPOSITORY_USERNAME"
    ensure_env_secret "flightctl-delta-generation-default-repository-password" "DELTA_GENERATION_DEFAULT_REPOSITORY_PASSWORD"

    # Quadlet does not support optional Secret= entries. Remove mounts for
    # credentials that were not configured so Podman does not try to use a
    # missing secret. Existing secrets are preserved for idempotent redeploys.
    local unit_dir="${QUADLET_FILES_OUTPUT_DIR:-/usr/share/containers/systemd}"
    local unit_file
    for unit_file in "${unit_dir}/flightctl-api.container" "${unit_dir}/flightctl-delta-worker.container"; do
        if [[ -f "$unit_file" ]]; then
            if ! sudo podman secret exists "flightctl-delta-generation-default-repository-username"; then
                sudo sed -i '/^Secret=flightctl-delta-generation-default-repository-username,/d' "$unit_file"
            fi
            if ! sudo podman secret exists "flightctl-delta-generation-default-repository-password"; then
                sudo sed -i '/^Secret=flightctl-delta-generation-default-repository-password,/d' "$unit_file"
            fi
        fi
    done
}

# Ensure a secret exists from an environment variable without generating a value.
ensure_env_secret() {
    local secret_name="$1"
    local env_var_name="$2"

    if sudo podman secret exists "$secret_name"; then
        return 0
    fi
    if [ -z "${!env_var_name}" ]; then
        echo "Skipping secret $secret_name because $env_var_name is not set"
        return 0
    fi
    echo "Creating secret $secret_name"
    sudo -E podman secret create --env "$secret_name" "$env_var_name"
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
