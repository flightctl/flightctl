#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-db-migrate configuration"

# Define paths
export SERVICE_CONFIG_FILE="/service-config.yaml"
ENV_TEMPLATE="/config-source/env.template"
ENV_OUTPUT="/config-destination/env"

# Check if service config file exists
if [ ! -f "$SERVICE_CONFIG_FILE" ]; then
  echo "Error: Service config file not found at $SERVICE_CONFIG_FILE"
  exit 1
fi

# Extract database configuration
DB_EXTERNAL=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*external:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)
if [ "$DB_EXTERNAL" == "enabled" ]; then
  echo "Configuring external database migration"
  DB_MIGRATION_USER=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*migrationUser:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)
  DB_MIGRATION_PASSWORD=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*migrationPassword:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)

  # Use defaults if not found
  DB_MIGRATION_USER=${DB_MIGRATION_USER:-flightctl_migrator}

  # For external database: read user and password directly from YAML, don't use Podman secrets
  DB_MIGRATION_PASSWORD_ENV="DB_MIGRATION_USER=$DB_MIGRATION_USER
DB_PASSWORD=$DB_MIGRATION_PASSWORD
DB_MIGRATION_PASSWORD=$DB_MIGRATION_PASSWORD"
else
  echo "Internal database - no environment file needed"
  # For internal database: password will come from Podman secret, don't set in env file
  DB_MIGRATION_PASSWORD_ENV=""
fi

# Write the environment file
if [ -n "$DB_MIGRATION_PASSWORD_ENV" ]; then
    # For external database, write the environment variables
    echo "$DB_MIGRATION_PASSWORD_ENV" > "$ENV_OUTPUT"
else
    # For internal database, create empty env file (password comes from Podman secret)
    touch "$ENV_OUTPUT"
fi

echo "Migration initialization complete"