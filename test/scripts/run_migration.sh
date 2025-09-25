#!/usr/bin/env bash

set -euo pipefail

# FlightCtl Test Migration Runner
# Runs the migration container with proper environment and configuration
# Optionally creates a template database for tests

# Configuration variables with defaults
MIGRATION_IMAGE=${MIGRATION_IMAGE:-localhost/flightctl-db-setup:latest}
CREATE_TEMPLATE=${CREATE_TEMPLATE:-false}
TEMPLATE_DB_NAME=${TEMPLATE_DB_NAME:-flightctl_tmpl}

DB_CONTAINER=${DB_CONTAINER:-flightctl-db}

DB_NAME=${DB_NAME:-flightctl}
DB_HOST=${DB_HOST:-localhost}
DB_PORT=${DB_PORT:-5432}

APP_USER=${DB_APP_USER:-flightctl_app}
DB_ADMIN_USER=${DB_ADMIN_USER:-admin}
DB_ADMIN_PASSWORD=${FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD:-adminpass}
DB_MIGRATION_USER=${DB_MIGRATION_USER:-flightctl_migrator}
DB_MIGRATION_PASSWORD=${FLIGHTCTL_POSTGRESQL_MIGRATOR_PASSWORD:-adminpass}

echo "Running database migration with image: $MIGRATION_IMAGE"
echo "Target database: $DB_NAME"

podman run --rm --network host \
    --pull=missing \
    -e DB_HOST="$DB_HOST" \
    -e DB_PORT="$DB_PORT" \
    -e DB_NAME="$DB_NAME" \
    -e DB_ADMIN_USER="$DB_ADMIN_USER" \
    -e DB_ADMIN_PASSWORD="$DB_ADMIN_PASSWORD" \
    -e DB_MIGRATION_USER="$DB_MIGRATION_USER" \
    -e DB_MIGRATION_PASSWORD="$DB_MIGRATION_PASSWORD" \
    "$MIGRATION_IMAGE" \
    /app/deploy/scripts/migration-setup.sh

# Create template database if requested
if [[ "$CREATE_TEMPLATE" == "true" ]]; then
    echo "Creating template database for tests..."
    echo "Template: $TEMPLATE_DB_NAME (from $DB_NAME)"
    
    # Function to execute SQL command via container
    execute_sql() {
        local sql_command="$1"
        local output
        local exit_code
        
        output=$(podman exec "$DB_CONTAINER" psql -U "$DB_ADMIN_USER" -d postgres -c "$sql_command" 2>&1)
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

