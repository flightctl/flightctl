#!/usr/bin/env bash

set -euo pipefail

# FlightCtl Test Migration Runner
# Runs the migration container with proper environment and configuration
# Optionally creates a template database for tests

# Configuration variables with defaults
DB_NAME=${DB_NAME:-flightctl}
MIGRATION_IMAGE=${MIGRATION_IMAGE:-localhost/flightctl-db-setup:latest}
CONFIG_FILE=${CONFIG_FILE:-}
CREATE_TEMPLATE=${CREATE_TEMPLATE:-false}
TEMPLATE_DB_NAME=${TEMPLATE_DB_NAME:-flightctl_tmpl}
DB_CONTAINER=${DB_CONTAINER:-flightctl-db}
TEST_USER=${TEST_USER:-flightctl_app}

# Check required environment variables
if [[ -z "$DB_USER" || -z "$DB_PASSWORD" || -z "$DB_MIGRATION_USER" || -z "$DB_MIGRATION_PASSWORD" ]]; then
    echo "Error: Required environment variables not set:"
    echo "  DB_USER, DB_PASSWORD, DB_MIGRATION_USER, DB_MIGRATION_PASSWORD"
    exit 1
fi

if [[ -z "$CONFIG_FILE" ]]; then
    echo "Error: CONFIG_FILE must be specified"
    exit 1
fi

if [[ ! -f "$CONFIG_FILE" ]]; then
    echo "Error: Config file not found: $CONFIG_FILE"
    exit 1
fi

echo "Running database migration with image: $MIGRATION_IMAGE"
echo "Target database: $DB_NAME"


sudo -E podman run --rm --network host \
    -e DB_USER="$DB_USER" \
    -e DB_PASSWORD="$DB_PASSWORD" \
    -e DB_MIGRATION_USER="$DB_MIGRATION_USER" \
    -e DB_MIGRATION_PASSWORD="$DB_MIGRATION_PASSWORD" \
    -v "$CONFIG_FILE:/root/.flightctl/config.yaml:ro,z" \
    --pull=missing \
    "$MIGRATION_IMAGE" \
    /usr/local/bin/flightctl-db-migrate

# Create template database if requested
if [[ "$CREATE_TEMPLATE" == "true" ]]; then
    echo "Creating template database for tests..."
    echo "Template: $TEMPLATE_DB_NAME (from $DB_NAME)"
    
    # Function to execute SQL command via container
    execute_sql() {
        local sql_command="$1"
        sudo podman exec "$DB_CONTAINER" psql -U "$DB_USER" -d postgres -c "$sql_command"
    }
    
    # Drop existing template database if it exists
    echo "Dropping existing template database if it exists..."
    execute_sql "DROP DATABASE IF EXISTS $TEMPLATE_DB_NAME;"
    
    # Create template database from source
    echo "Creating template database from $DB_NAME..."
    execute_sql "CREATE DATABASE $TEMPLATE_DB_NAME TEMPLATE $DB_NAME;"
    
    # Grant ownership to test user
    echo "Granting ownership to $TEST_USER..."
    execute_sql "ALTER DATABASE $TEMPLATE_DB_NAME OWNER TO $TEST_USER;"
    
    echo "Template database $TEMPLATE_DB_NAME created successfully!"
fi
