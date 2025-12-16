#!/usr/bin/env bash

set -eo pipefail

# Flight Control Database User Setup Script
# This script creates the database users with appropriate permissions for production deployments

# Configuration variables
DB_HOST=${DB_HOST:-localhost}
DB_PORT=${DB_PORT:-5432}
DB_NAME=${DB_NAME:-flightctl}
DB_ADMIN_USER=${DB_ADMIN_USER:-admin}
DB_ADMIN_PASSWORD=${DB_ADMIN_PASSWORD:-adminpass}
DB_MIGRATION_USER=${DB_MIGRATION_USER:-flightctl_migrator}
DB_MIGRATION_PASSWORD=${DB_MIGRATION_PASSWORD:-migrator_password}
DB_APP_USER=${DB_APP_USER:-flightctl_app}
DB_APP_PASSWORD=${DB_APP_PASSWORD:-app_password}

# PostgreSQL image to use for database connections
POSTGRES_IMAGE=${POSTGRES_IMAGE:-quay.io/sclorg/postgresql-16-c9s:latest}

# Function to execute SQL command using podman
execute_sql_command() {
    local sql_command="$1"
    local use_network=${2:-true}

    local network_arg=""
    if [ "$use_network" = "true" ]; then
        network_arg="--network flightctl"
    fi

    # Build SSL environment arguments
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

    sudo podman run --rm $network_arg \
        -e PGPASSWORD="$DB_ADMIN_PASSWORD" \
        $ssl_args \
        "$POSTGRES_IMAGE" \
        psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "$sql_command"
}

# Function to execute SQL file with environment variable substitution
execute_sql_file() {
    local sql_file="$1"
    local use_network=${2:-true}

    if [[ ! -f "$sql_file" ]]; then
        echo "Error: SQL file not found: $sql_file"
        exit 1
    fi

    echo "Executing SQL file: $sql_file"

    # Create a temporary file with environment variable substitution
    local temp_sql_file
    temp_sql_file=$(mktemp)
    envsubst < "$sql_file" > "$temp_sql_file"

    local network_arg=""
    if [ "$use_network" = "true" ]; then
        network_arg="--network flightctl"
    fi

    # Build SSL environment arguments
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

    # Execute the SQL file using podman with stdin
    sudo podman run --rm $network_arg \
        -e PGPASSWORD="$DB_ADMIN_PASSWORD" \
        $ssl_args \
        -i \
        "$POSTGRES_IMAGE" \
        psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_ADMIN_USER" -d "$DB_NAME" < "$temp_sql_file"

    # Clean up temporary file
    rm -f "$temp_sql_file"
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

# Export environment variables for substitution
export DB_NAME DB_MIGRATION_USER DB_MIGRATION_PASSWORD DB_APP_USER DB_APP_PASSWORD

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
