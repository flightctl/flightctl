#!/usr/bin/env bash

set -euo pipefail

echo "Initializing flightctl-alertmanager-proxy configuration"

source "/utils/init_utils.sh"

# Define paths
export SERVICE_CONFIG_FILE="/service-config.yaml"
ENV_TEMPLATE="/config-source/env.template"
ENV_OUTPUT="/config-destination/env"

# Check if service config file exists
if [ ! -f "$SERVICE_CONFIG_FILE" ]; then
  echo "Error: Service config file not found at $SERVICE_CONFIG_FILE"
  exit 1
fi

# Extract auth-related values
AUTH_TYPE=$(extract_value "global.auth.type" "$SERVICE_CONFIG_FILE" || true)
if [ -z "$AUTH_TYPE" ]; then
  echo "Error: unable to determine auth.type from $SERVICE_CONFIG_FILE"
  exit 1
fi

# Translate "builtin" to "oidc" for backwards compatibility
# builtin is legacy auth that uses OIDC with PAM issuer enabled
if [ "$AUTH_TYPE" == "builtin" ]; then
  echo "Auth type 'builtin' detected - translating to 'oidc'"
  AUTH_TYPE="oidc"
fi

FLIGHTCTL_DISABLE_AUTH=""

# Process auth settings based on auth type
if [ "$AUTH_TYPE" == "aap" ] || [ "$AUTH_TYPE" == "oidc" ] || [ "$AUTH_TYPE" == "k8s" ]; then
  echo "Auth configured with type: $AUTH_TYPE"
  FLIGHTCTL_DISABLE_AUTH=""
else
  echo "Auth not configured"
  FLIGHTCTL_DISABLE_AUTH="true"
fi

# Extract database configuration
DB_EXTERNAL=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*external:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)
if [ "$DB_EXTERNAL" == "enabled" ]; then
  echo "External database - password will come from Podman secret"
else
  echo "Internal database - password will come from Podman secret"
fi

# Template the environment file
mkdir -p "$(dirname "$ENV_OUTPUT")"
sed -e "s|{{FLIGHTCTL_DISABLE_AUTH}}|$FLIGHTCTL_DISABLE_AUTH|g" \
    "$ENV_TEMPLATE" > "$ENV_OUTPUT"

echo "Initialization complete" 