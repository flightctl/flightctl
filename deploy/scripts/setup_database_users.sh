#!/usr/bin/env bash

set -eo pipefail

# Flight Control Database User Setup Script
# This script creates the database users with appropriate permissions for production deployments.
#
# Modes:
#   Default (podman): wraps psql calls inside a podman container (for host execution)
#   --direct:         calls psql directly (for execution inside a container that has psql)
#
# Credentials can be supplied via environment variables (DB_ADMIN_USER, DB_ADMIN_PASSWORD, etc.)
# or via secret-mounted files at well-known default paths under /run/secrets/.
# The environment variable takes precedence; if unset, the default file path is tried.
# The script exits with an error if a value is not provided by either mechanism.
# Passwords are never passed on the psql command line; they are fed via stdin (\set).

DIRECT_PSQL=false
while [[ $# -gt 0 ]]; do
    case $1 in
        --direct)
            DIRECT_PSQL=true
            shift
            ;;
        *)
            echo "Unknown argument: $1"
            exit 1
            ;;
    esac
done

# Configuration variables
DB_HOST=${DB_HOST:-localhost}
DB_PORT=${DB_PORT:-5432}
DB_NAME=${DB_NAME:-flightctl}

# Resolve value: prefer env var, fall back to default secret file path, error if neither.
resolve_value() {
    local name="$1" default_file="$2" env_val="${3:-}"
    if [[ -n "${env_val}" ]]; then
        echo "$env_val"
    elif [[ -f "${default_file}" && -r "${default_file}" ]]; then
        cat "$default_file"
    else
        echo "Error: ${name} must be set via environment variable or available at ${default_file}" >&2
        exit 1
    fi
}

DB_ADMIN_USER="$(resolve_value DB_ADMIN_USER /run/secrets/db-admin/masterUser "${DB_ADMIN_USER:-}")"
DB_MIGRATION_USER="$(resolve_value DB_MIGRATION_USER /run/secrets/db-migration/migrationUser "${DB_MIGRATION_USER:-}")"
DB_APP_USER="$(resolve_value DB_APP_USER /run/secrets/db/user "${DB_APP_USER:-}")"

DB_ADMIN_PASSWORD="$(resolve_value DB_ADMIN_PASSWORD /run/secrets/db-admin/masterPassword "${DB_ADMIN_PASSWORD:-}")"
DB_MIGRATION_PASSWORD="$(resolve_value DB_MIGRATION_PASSWORD /run/secrets/db-migration/migrationPassword "${DB_MIGRATION_PASSWORD:-}")"
DB_APP_PASSWORD="$(resolve_value DB_APP_PASSWORD /run/secrets/db/userPassword "${DB_APP_PASSWORD:-}")"

# PostgreSQL image to use for database connections (podman mode only)
POSTGRES_IMAGE=${POSTGRES_IMAGE:-quay.io/sclorg/postgresql-16-c9s:latest}

# Build SSL environment arguments for podman mode
build_ssl_env_args() {
    local ssl_args=""
    if [[ -n "${PGSSLMODE:-}" ]]; then
        ssl_args="$ssl_args -e PGSSLMODE=$PGSSLMODE"
    fi
    if [[ -n "${PGSSLCERT:-}" ]]; then
        ssl_args="$ssl_args -e PGSSLCERT=$PGSSLCERT"
    fi
    if [[ -n "${PGSSLKEY:-}" ]]; then
        ssl_args="$ssl_args -e PGSSLKEY=$PGSSLKEY"
    fi
    if [[ -n "${PGSSLROOTCERT:-}" ]]; then
        ssl_args="$ssl_args -e PGSSLROOTCERT=$PGSSLROOTCERT"
    fi
    echo "$ssl_args"
}

# Function to execute a single SQL command
execute_sql_command() {
    local sql_command="$1"
    local use_network=${2:-true}

    if [[ "$DIRECT_PSQL" == "true" ]]; then
        PGPASSWORD="$DB_ADMIN_PASSWORD" \
        psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "$sql_command"
        return
    fi

    local network_arg=""
    if [ "$use_network" = "true" ]; then
        network_arg="--network flightctl"
    fi

    local ssl_args
    ssl_args="$(build_ssl_env_args)"

    sudo podman run --rm $network_arg \
        -e PGPASSWORD="$DB_ADMIN_PASSWORD" \
        $ssl_args \
        "$POSTGRES_IMAGE" \
        psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "$sql_command"
}

# Function to execute SQL file with passwords piped via stdin (never on the command line).
# Non-sensitive identifiers (db_name, user names) are passed via -v.
execute_sql_file() {
    local sql_file="$1"
    local use_network=${2:-true}

    if [[ ! -f "$sql_file" ]]; then
        echo "Error: SQL file not found: $sql_file"
        exit 1
    fi

    echo "Executing SQL file: $sql_file"

    if [[ "$DIRECT_PSQL" == "true" ]]; then
        {
            printf "\\set migration_password '%s'\n" "${DB_MIGRATION_PASSWORD//\'/\'\'}"
            printf "\\set app_password '%s'\n" "${DB_APP_PASSWORD//\'/\'\'}"
            cat "$sql_file"
        } | PGPASSWORD="$DB_ADMIN_PASSWORD" \
            psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_ADMIN_USER" -d "$DB_NAME" \
                -v db_name="$DB_NAME" \
                -v migration_user="$DB_MIGRATION_USER" \
                -v app_user="$DB_APP_USER"
        return
    fi

    local network_arg=""
    if [ "$use_network" = "true" ]; then
        network_arg="--network flightctl"
    fi

    local ssl_args
    ssl_args="$(build_ssl_env_args)"

    {
        printf "\\set migration_password '%s'\n" "${DB_MIGRATION_PASSWORD//\'/\'\'}"
        printf "\\set app_password '%s'\n" "${DB_APP_PASSWORD//\'/\'\'}"
        cat "$sql_file"
    } | sudo podman run --rm -i $network_arg \
        -e PGPASSWORD="$DB_ADMIN_PASSWORD" \
        $ssl_args \
        "$POSTGRES_IMAGE" \
        psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_ADMIN_USER" -d "$DB_NAME" \
            -v db_name="$DB_NAME" \
            -v migration_user="$DB_MIGRATION_USER" \
            -v app_user="$DB_APP_USER"
}

# Check if database is accessible
echo "Checking database connection..."
if ! execute_sql_command "SELECT 1" >/dev/null 2>&1; then
    echo "Error: Cannot connect to database. Please check your database configuration."
    exit 1
fi

echo "Setting up Flight Control database users..."

# Find the SQL file relative to this script
SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
SQL_FILE="$SCRIPT_DIR/setup_database_users.sql"

# Execute the canonical SQL file
execute_sql_file "$SQL_FILE"

echo "Database user setup completed successfully!"
echo "Migration user: $DB_MIGRATION_USER (full privileges)"
echo "Application user: $DB_APP_USER (limited privileges)"
echo ""
echo "Set the following environment variables in your services:"
echo "  DB_USER=$DB_APP_USER"
echo "  DB_PASSWORD=<configured_password>"
echo "  DB_MIGRATION_USER=$DB_MIGRATION_USER"
echo "  DB_MIGRATION_PASSWORD=<configured_password>"
