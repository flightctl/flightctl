#!/usr/bin/env bash

set -euo pipefail

# Script parameters
IMAGE_TAG="${1:-latest}"
SERVICE_CONFIG_PATH="${2:-/etc/flightctl/flightctl-api/config.yaml}"

# Tool paths
PODMAN="${PODMAN:-$(command -v podman || echo '/usr/bin/podman')}"
YQ="${YQ:-$(command -v yq || echo '/usr/bin/yq')}"

# Configuration
DB_SETUP_IMAGE="quay.io/flightctl/flightctl-db-setup:${IMAGE_TAG}"
INSTALL_CONFIG_FILE="/etc/flightctl/flightctl-services-install.conf"
SERVICE_CONFIG_DIR="/etc/flightctl"

# Load installation configuration file if exists
load_install_config() {
    # shellcheck source=/etc/flightctl/flightctl-services-install.conf
    if [ -f "${INSTALL_CONFIG_FILE}" ]; then
        . "${INSTALL_CONFIG_FILE}"
    fi

    # Set defaults from installation config or environment
    RUN_DRY_RUN="${RUN_DRY_RUN:-${FLIGHTCTL_MIGRATION_DRY_RUN:-0}}"
    DB_WAIT_TIMEOUT="${DB_WAIT_TIMEOUT:-${FLIGHTCTL_DB_WAIT_TIMEOUT:-60}}"
    DB_WAIT_SLEEP="${DB_WAIT_SLEEP:-${FLIGHTCTL_DB_WAIT_SLEEP:-1}}"
}

# Parse database configuration from service YAML config
parse_service_db_config() {
    if [ ! -f "${SERVICE_CONFIG_PATH}" ]; then
        echo "[flightctl] service config file not found: ${SERVICE_CONFIG_PATH}; skipping"
        exit 0
    fi

    echo "[flightctl] parsing database config from service config: ${SERVICE_CONFIG_PATH}"
    DB_HOST=$(${YQ} '.database.hostname // "flightctl-db"' "${SERVICE_CONFIG_PATH}")
    DB_PORT=$(${YQ} '.database.port // 5432' "${SERVICE_CONFIG_PATH}")
    DB_NAME=$(${YQ} '.database.name // "flightctl"' "${SERVICE_CONFIG_PATH}")
    DB_USER=${DB_USER:-$(${YQ} '.database.migrationUser // "flightctl_migrator"' "${SERVICE_CONFIG_PATH}")}
    
    # Parse SSL configuration
    DB_SSLMODE=$(${YQ} '.database.sslmode // ""' "${SERVICE_CONFIG_PATH}")
    DB_SSLCERT=$(${YQ} '.database.sslcert // ""' "${SERVICE_CONFIG_PATH}")
    DB_SSLKEY=$(${YQ} '.database.sslkey // ""' "${SERVICE_CONFIG_PATH}")
    DB_SSLROOTCERT=$(${YQ} '.database.sslrootcert // ""' "${SERVICE_CONFIG_PATH}")

    echo -n "[flightctl] database config: host=${DB_HOST}, port=${DB_PORT}, name=${DB_NAME}, user=${DB_USER}"
    if [ -n "${DB_SSLMODE}" ]; then
        echo ", sslmode=${DB_SSLMODE}"
    else
        echo ""
    fi
}


# Wait for database to be ready
wait_for_database() {
    echo "[flightctl] waiting for database (timeout=${DB_WAIT_TIMEOUT}s sleep=${DB_WAIT_SLEEP}s)"

    local podman_args=()
    podman_args+=("--rm" "--network" "flightctl")
    podman_args+=("-e" "PGHOST=${DB_HOST}")
    podman_args+=("-e" "PGPORT=${DB_PORT}")
    podman_args+=("-e" "PGDATABASE=${DB_NAME}")
    podman_args+=("-e" "PGUSER=${DB_USER}")
    
    # Add SSL environment variables if set
    [ -n "${DB_SSLMODE}" ] && podman_args+=("-e" "PGSSLMODE=${DB_SSLMODE}")
    [ -n "${DB_SSLCERT}" ] && podman_args+=("-e" "PGSSLCERT=${DB_SSLCERT}")
    [ -n "${DB_SSLKEY}" ] && podman_args+=("-e" "PGSSLKEY=${DB_SSLKEY}")
    [ -n "${DB_SSLROOTCERT}" ] && podman_args+=("-e" "PGSSLROOTCERT=${DB_SSLROOTCERT}")
    
    podman_args+=("--secret" "flightctl-postgresql-migrator-password,type=env,target=PGPASSWORD")
    podman_args+=("${DB_SETUP_IMAGE}")
    podman_args+=("/app/deploy/scripts/wait-for-database.sh")
    podman_args+=("--timeout=${DB_WAIT_TIMEOUT}" "--sleep=${DB_WAIT_SLEEP}")

    if ! "${PODMAN}" run "${podman_args[@]}"; then
        echo "[flightctl] database wait failed; skipping dry-run"
        exit 0
    fi
}

# Run database migration dry-run
run_migration_dry_run() {
    echo "[flightctl] running database migration dry-run"

    if "${PODMAN}" run --rm --network flightctl \
        --secret flightctl-postgresql-migrator-password,type=env,target=DB_PASSWORD \
        --secret flightctl-postgresql-migrator-password,type=env,target=DB_MIGRATION_PASSWORD \
        -v "${SERVICE_CONFIG_PATH}":/root/.flightctl/config.yaml:ro,z \
        -v "${SERVICE_CONFIG_DIR}/service-config.yaml":/etc/flightctl/service-config.yaml:ro,z \
        "${DB_SETUP_IMAGE}" /usr/local/bin/flightctl-db-migrate --dry-run; then
        echo "[flightctl] dry-run completed successfully"
    else
        echo "[flightctl] dry-run failed"
        exit 1
    fi
}

# Main execution
main() {
    echo "[flightctl] pre-upgrade migration dry-run (tag=${IMAGE_TAG})"
    echo "[flightctl] using service config: ${SERVICE_CONFIG_PATH}"
    echo "[flightctl] using image: ${DB_SETUP_IMAGE}"

    load_install_config
    
    if [ "${RUN_DRY_RUN}" != "1" ]; then
        echo "[flightctl] dry-run disabled; skipping"
        exit 0
    fi
    
    parse_service_db_config
    wait_for_database
    run_migration_dry_run
}

main "$@"
