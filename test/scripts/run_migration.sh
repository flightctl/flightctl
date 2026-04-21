#!/usr/bin/env bash

set -euo pipefail

# FlightCtl Test Migration Runner
# Runs the migration container with proper environment and configuration
# Optionally creates a template database for tests

# Configuration variables with defaults
MIGRATION_IMAGE=${MIGRATION_IMAGE:-localhost/flightctl-db-setup:latest}
CREATE_TEMPLATE=${CREATE_TEMPLATE:-false}
TEMPLATE_DB_NAME=${TEMPLATE_DB_NAME:-flightctl_tmpl}

# DB_CONTAINER is retained for compatibility; CREATE_TEMPLATE uses psql inside MIGRATION_IMAGE (host/port) instead of podman exec.
DB_CONTAINER=${DB_CONTAINER:-flightctl-db}

DB_NAME=${DB_NAME:-flightctl}
DB_HOST=${DB_HOST:-localhost}
DB_PORT=${DB_PORT:-5432}

APP_USER=${DB_APP_USER:-flightctl_app}
DB_APP_PASSWORD=${FLIGHTCTL_POSTGRESQL_USER_PASSWORD:-adminpass}
DB_ADMIN_USER=${DB_ADMIN_USER:-admin}
DB_ADMIN_PASSWORD=${FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD:-adminpass}
DB_MIGRATION_USER=${DB_MIGRATION_USER:-flightctl_migrator}
DB_MIGRATION_PASSWORD=${FLIGHTCTL_POSTGRESQL_MIGRATOR_PASSWORD:-adminpass}

# When DOCKER_HOST is unset, detect the container socket (same order as
# test/harness/containers.detectContainerSocket) and set DOCKER_HOST so docker|podman
# CLI matches where testcontainers started integration Postgres.
is_container_socket() {
    [[ -e "$1" ]] && [[ -S "$1" ]]
}

detect_container_socket() {
    local p
    if [[ -n "${XDG_RUNTIME_DIR:-}" ]]; then
        p="${XDG_RUNTIME_DIR}/podman/podman.sock"
        is_container_socket "$p" && {
            echo "$p"
            return
        }
    fi
    if [[ "${UID:-0}" -ne 0 ]]; then
        p="/run/user/${UID}/podman/podman.sock"
        is_container_socket "$p" && {
            echo "$p"
            return
        }
    fi
    if [[ -n "${HOME:-}" ]]; then
        p="${HOME}/.local/share/containers/podman/machine/podman.sock"
        is_container_socket "$p" && {
            echo "$p"
            return
        }
    fi
    # Match test/harness/containers.detectContainerSocket (Docker before system Podman for unprivileged CI).
    p="/var/run/docker.sock"
    is_container_socket "$p" && {
        echo "$p"
        return
    }
    p="/run/podman/podman.sock"
    is_container_socket "$p" && {
        echo "$p"
        return
    }
    echo ""
}

configure_container_runtime_env() {
    [[ -n "${DOCKER_HOST:-}" ]] && return 0
    local sock
    sock=$(detect_container_socket)
    [[ -z "$sock" ]] && return 0
    export DOCKER_HOST="unix://${sock}"
}

# Same rule as test/harness/containers.RuntimeCLIName(): podman when DOCKER_HOST is unset or
# points at a podman socket; otherwise docker.
integration_container_cli() {
    local dh="${DOCKER_HOST:-}"
    if [[ -n "$dh" ]] && [[ "$dh" != *podman* ]]; then
        echo docker
        return
    fi
    echo podman
}

# Must run in this shell (not inside command substitution) so export DOCKER_HOST applies.
configure_container_runtime_env
CONTAINER_CLI=$(integration_container_cli)

# CLI for running the migration image: prefer whichever engine already has MIGRATION_IMAGE (CI builds
# with podman while testcontainers may use docker for integration Postgres).
migration_container_cli() {
    if command -v podman >/dev/null 2>&1 && podman image inspect "$MIGRATION_IMAGE" >/dev/null 2>&1; then
        echo podman
        return
    fi
    if command -v docker >/dev/null 2>&1 && docker image inspect "$MIGRATION_IMAGE" >/dev/null 2>&1; then
        echo docker
        return
    fi
    echo "${CONTAINER_CLI}"
}

MIGRATION_CLI=$(migration_container_cli)

# When flightctl-integration-postgres exists, use its published port (testcontainers / make start-integration-services).
apply_integration_postgres_published_port() {
    command -v "${CONTAINER_CLI}" >/dev/null 2>&1 || return 0

    if ! "${CONTAINER_CLI}" inspect flightctl-integration-postgres >/dev/null 2>&1; then
        return 0
    fi

    line=$("${CONTAINER_CLI}" port flightctl-integration-postgres 5432/tcp 2>/dev/null | head -1 | tr -d '\r' | tr -d '[:space:]')
    if [[ -z "$line" ]]; then
        return 0
    fi

    port="${line##*:}"
    host="${line%:*}"
    host="${host#[}"
    host="${host%]}"
    if [[ "$host" == "0.0.0.0" || "$host" == "::" ]]; then
        host=127.0.0.1
    fi
    export DB_HOST="$host"
    export DB_PORT="$port"
    export DB_ADMIN_USER=postgres
    echo "Using integration Postgres at ${DB_HOST}:${DB_PORT} (${CONTAINER_CLI} inspect/port, flightctl-integration-postgres)"
}

apply_integration_postgres_published_port || exit 1

echo "Running database migration with image: $MIGRATION_IMAGE"
echo "Target database: $DB_NAME"

# Step 1: Run setup_database_users.sql (matching production Helm/Quadlet flow)
echo "Setting up database users..."
"${MIGRATION_CLI}" run --rm --network host \
    --pull=missing \
    -e DB_HOST="$DB_HOST" \
    -e DB_PORT="$DB_PORT" \
    -e DB_NAME="$DB_NAME" \
    -e DB_ADMIN_USER="$DB_ADMIN_USER" \
    -e DB_ADMIN_PASSWORD="$DB_ADMIN_PASSWORD" \
    -e DB_MIGRATION_USER="$DB_MIGRATION_USER" \
    -e DB_MIGRATION_PASSWORD="$DB_MIGRATION_PASSWORD" \
    -e DB_APP_USER="$APP_USER" \
    -e DB_APP_PASSWORD="$DB_APP_PASSWORD" \
    -e PGPASSWORD="$DB_ADMIN_PASSWORD" \
    "$MIGRATION_IMAGE" \
    /bin/bash -c 'envsubst < /app/deploy/scripts/setup_database_users.sql | psql -v ON_ERROR_STOP=1 -h "$DB_HOST" -p "$DB_PORT" -U "$DB_ADMIN_USER" -d "$DB_NAME"'

# Step 2: Run flightctl-db-migrate binary (same as production)
echo "Running database migrations..."
"${MIGRATION_CLI}" run --rm --network host \
    -e DB_HOST="$DB_HOST" \
    -e DB_PORT="$DB_PORT" \
    -e DB_NAME="$DB_NAME" \
    -e DB_USER="$DB_MIGRATION_USER" \
    -e DB_PASSWORD="$DB_MIGRATION_PASSWORD" \
    "$MIGRATION_IMAGE" \
    /usr/local/bin/flightctl-db-migrate

# Create template database if requested
if [[ "$CREATE_TEMPLATE" == "true" ]]; then
    echo "Creating template database for tests..."
    echo "Template: $TEMPLATE_DB_NAME (from $DB_NAME)"
    
    # Run SQL against Postgres using the migration image (psql), so we work with any
    # reachable DB (e.g. testcontainers on localhost) without podman exec into DB_CONTAINER.
    execute_sql() {
        local sql_command="$1"
        local output
        local exit_code

        output=$("${MIGRATION_CLI}" run --rm --network host \
            -e PGPASSWORD="$DB_ADMIN_PASSWORD" \
            "$MIGRATION_IMAGE" \
            psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_ADMIN_USER" -d postgres -v ON_ERROR_STOP=1 -c "$sql_command" 2>&1)
        exit_code=$?

        if [[ $exit_code -ne 0 ]]; then
            echo "ERROR: SQL command failed with exit code $exit_code" >&2
            echo "SQL command: $sql_command" >&2
            echo "Output: $output" >&2
            exit 1
        fi

        echo "$output"
    }
    
    # Drop existing template database if it exists
    echo "Dropping existing template database if it exists..."
    execute_sql "DROP DATABASE IF EXISTS $TEMPLATE_DB_NAME;"
    
    # Create template database from source
    echo "Creating template database from $DB_NAME..."
    execute_sql "CREATE DATABASE $TEMPLATE_DB_NAME TEMPLATE $DB_NAME;"
    
    # Grant ownership to application user
    echo "Granting ownership to $APP_USER..."
    execute_sql "ALTER DATABASE $TEMPLATE_DB_NAME OWNER TO $APP_USER;"
    
    echo "Template database $TEMPLATE_DB_NAME created successfully!"
fi

