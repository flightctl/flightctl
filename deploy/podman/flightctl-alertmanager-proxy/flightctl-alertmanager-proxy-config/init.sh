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
AUTH_TYPE=$(extract_value "type" "$SERVICE_CONFIG_FILE" || true)
if [ -z "$AUTH_TYPE" ]; then
  echo "Error: unable to determine auth.type from $SERVICE_CONFIG_FILE"
  exit 1
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

# Extract rate limit values (defaults if not configured)
RATE_LIMIT_REQUESTS=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[[:space:]]*[^[:space:]]/p' | sed -n 's/^[[:space:]]*requests:[[:space:]]*\([^[:space:]]*\).*/\1/p' | head -1)
RATE_LIMIT_WINDOW=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[[:space:]]*[^[:space:]]/p' | sed -n 's/^[[:space:]]*window:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)
AUTH_RATE_LIMIT_REQUESTS=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[[:space:]]*[^[:space:]]/p' | sed -n 's/^[[:space:]]*authRequests:[[:space:]]*\([^[:space:]]*\).*/\1/p' | head -1)
AUTH_RATE_LIMIT_WINDOW=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[[:space:]]*[^[:space:]]/p' | sed -n 's/^[[:space:]]*authWindow:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)

# Use defaults if not found
RATE_LIMIT_REQUESTS=${RATE_LIMIT_REQUESTS:-60}
RATE_LIMIT_WINDOW=${RATE_LIMIT_WINDOW:-1m}
AUTH_RATE_LIMIT_REQUESTS=${AUTH_RATE_LIMIT_REQUESTS:-10}
AUTH_RATE_LIMIT_WINDOW=${AUTH_RATE_LIMIT_WINDOW:-1h}

# Template the environment file
mkdir -p "$(dirname "$ENV_OUTPUT")"
sed -e "s|{{FLIGHTCTL_DISABLE_AUTH}}|$FLIGHTCTL_DISABLE_AUTH|g" \
    -e "s|{{RATE_LIMIT_REQUESTS}}|$RATE_LIMIT_REQUESTS|g" \
    -e "s|{{RATE_LIMIT_WINDOW}}|$RATE_LIMIT_WINDOW|g" \
    -e "s|{{AUTH_RATE_LIMIT_REQUESTS}}|$AUTH_RATE_LIMIT_REQUESTS|g" \
    -e "s|{{AUTH_RATE_LIMIT_WINDOW}}|$AUTH_RATE_LIMIT_WINDOW|g" \
    "$ENV_TEMPLATE" > "$ENV_OUTPUT"

echo "Initialization complete" 