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

    if [ ! -x "${YQ}" ]; then
        echo "[flightctl] yq not found; using default database values"
        DB_HOST="flightctl-db"
        DB_PORT="5432"
        DB_NAME="flightctl"
        DB_USER="admin"
    else
        echo "[flightctl] parsing database config from service config: ${SERVICE_CONFIG_PATH}"
        DB_HOST=$(${YQ} '.database.hostname // "flightctl-db"' "${SERVICE_CONFIG_PATH}")
        DB_PORT=$(${YQ} '.database.port // 5432' "${SERVICE_CONFIG_PATH}")
        DB_NAME=$(${YQ} '.database.name // "flightctl"' "${SERVICE_CONFIG_PATH}")
        DB_USER=$(${YQ} '.database.user // "admin"' "${SERVICE_CONFIG_PATH}")
    fi

    echo "[flightctl] database config: host=${DB_HOST}, port=${DB_PORT}, name=${DB_NAME}, user=${DB_USER}"
}

# Check prerequisites
check_prerequisites() {
    if [ "${RUN_DRY_RUN}" != "1" ]; then
        echo "[flightctl] dry-run disabled; skipping"
        exit 0
    fi

    if [ ! -x "${PODMAN}" ]; then
        echo "[flightctl] podman not found; skipping"
        exit 0
    fi

    if ! "${PODMAN}" container exists flightctl-db >/dev/null 2>&1; then
        echo "[flightctl] database container not found; skipping"
        exit 0
    fi
}

# Wait for database to be ready
wait_for_database() {
    echo "[flightctl] waiting for database (timeout=${DB_WAIT_TIMEOUT}s sleep=${DB_WAIT_SLEEP}s)"

    if ! "${PODMAN}" run --rm --network flightctl \
        -e DB_HOST="${DB_HOST}" \
        -e DB_PORT="${DB_PORT}" \
        -e DB_NAME="${DB_NAME}" \
        -e DB_USER="${DB_USER}" \
        --secret flightctl-postgresql-master-password,type=env,target=DB_PASSWORD \
        "${DB_SETUP_IMAGE}" /app/deploy/scripts/wait-for-database.sh \
        --timeout="${DB_WAIT_TIMEOUT}" --sleep="${DB_WAIT_SLEEP}"; then
        echo "[flightctl] database wait failed; skipping dry-run"
        exit 0
    fi
}

# Run database migration dry-run
run_migration_dry_run() {
    echo "[flightctl] running database migration dry-run"

    if "${PODMAN}" run --rm --network flightctl \
        -e DB_MIGRATION_USER=flightctl_migrator \
        --secret flightctl-postgresql-migrator-password,type=env,target=DB_MIGRATION_PASSWORD \
        -v "${SERVICE_CONFIG_PATH}":/root/.flightctl/config.yaml:ro,z \
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

    load_install_config
    parse_service_db_config
    check_prerequisites
    wait_for_database
    run_migration_dry_run
}

main "$@"
