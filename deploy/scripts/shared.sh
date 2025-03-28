#!/usr/bin/env bash

# Output directory paths
readonly CONFIG_OUTPUT_DIR="$HOME/.config/flightctl"
readonly QUADLET_FILES_OUTPUT_DIR="$HOME/.config/containers/systemd"
export CONFIG_OUTPUT_DIR

# Function to substitute environment variables in a template
# Args:
#   $1: Source template file
#   $2: Destination file
inject_vars() {
    local source_file="$1"
    local dest_file="$2"

    envsubst '$BASE_DOMAIN $CONFIG_OUTPUT_DIR $FLIGHTCTL_DISABLE_AUTH' < "$source_file" > "$dest_file"
}

# Render a service configuration
# Args:
#   $1: Service name
#   $2: Template directory path
#   $3: "standalone" if using standalone mode (optional)
render_service() {
    local service_name="$1"
    local template_dir="$2"
    local standalone="$3"

    # Determine container template file
    local container_file="${template_dir}/flightctl-${service_name}/flightctl-${service_name}.container"
    if [[ "$standalone" == "standalone" ]]; then
        container_file="${template_dir}/flightctl-${service_name}/flightctl-${service_name}-standalone.container"
    fi

    # Ensure quadlet output directory exists
    mkdir -p "${QUADLET_FILES_OUTPUT_DIR}"
    # Ensure config output directory exists
    mkdir -p "${CONFIG_OUTPUT_DIR}/flightctl-${service_name}"

    # Process container template
    inject_vars "$container_file" "${QUADLET_FILES_OUTPUT_DIR}/flightctl-${service_name}.container"

    # Process all files in the config directory
    for config_file in "${template_dir}/flightctl-${service_name}/flightctl-${service_name}-config"/*; do
        if [[ -f "$config_file" ]]; then
            inject_vars "$config_file" "${CONFIG_OUTPUT_DIR}/flightctl-${service_name}/$(basename "$config_file")"
        fi
    done

    # Move any .volume file if it exists
    for volume in "${template_dir}/flightctl-${service_name}"/*.volume; do
        if [[ -f "$volume" ]]; then
            cp "$volume" "${QUADLET_FILES_OUTPUT_DIR}"
        fi
    done
}

# Start a systemd service
# Args:
#   $1: Service name
start_service() {
    local service_name="$1"
    systemctl --user daemon-reload

    echo "Starting $service_name"
    systemctl --user start "$service_name"
}

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
