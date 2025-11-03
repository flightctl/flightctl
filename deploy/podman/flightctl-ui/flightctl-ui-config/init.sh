#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-ui configuration"

source "/utils/init_utils.sh"

# Mounted volumes in the container
CERTS_SOURCE_PATH="/certs-source"
CERTS_DEST_PATH="/certs-destination"
SERVICE_CONFIG_FILE="/service-config.yaml"
ENV_TEMPLATE="/config-source/env.template"

# ENV file to be used by the application
ENV_OUTPUT="/config-destination/env"

# Check if service config file exists
if [ ! -f "$SERVICE_CONFIG_FILE" ]; then
  echo "Error: Service config file not found at $SERVICE_CONFIG_FILE"
  exit 1
fi

# Extract base values from service-config.yaml
BASE_DOMAIN=$(extract_value "global.baseDomain" "$SERVICE_CONFIG_FILE")

# Extract auth-related values
AUTH_TYPE=$(extract_value "global.auth.type" "$SERVICE_CONFIG_FILE")

# Translate "builtin" to "oidc" for backwards compatibility
# builtin is legacy auth that uses OIDC with PAM issuer enabled
if [ "$AUTH_TYPE" == "builtin" ]; then
  echo "Auth type 'builtin' detected - translating to 'oidc'"
  AUTH_TYPE="oidc"
fi

AUTH_INSECURE_SKIP_VERIFY=$(extract_value "global.auth.insecureSkipTlsVerify" "$SERVICE_CONFIG_FILE")
AUTH_CLIENT_ID=""
AUTH_URL=""

# Extract organizations enabled value (defaults to false if not configured)
ORGANIZATIONS_ENABLED=$(sed -n '/^global:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*organizations:/,/^[^[:space:]]/p' | sed -n '/^[[:space:]]*enabled:[[:space:]]*\([^[:space:]]*\).*/s//\1/p' | head -1)
ORGANIZATIONS_ENABLED=${ORGANIZATIONS_ENABLED:-false}

# Verify required values were found
if [ -z "$BASE_DOMAIN" ]; then
  echo "Error: Could not find baseDomain in service config file"
  exit 1
fi

# Process auth settings based on auth type
if [ "$AUTH_TYPE" == "aap" ]; then
  echo "Configuring AAP authentication"
  AUTH_CLIENT_ID=$(extract_value "global.auth.aap.oAuthApplicationClientId" "$SERVICE_CONFIG_FILE")
  AUTH_URL=$(extract_value "global.auth.aap.apiUrl" "$SERVICE_CONFIG_FILE")
elif [ "$AUTH_TYPE" == "oidc" ]; then
  echo "Configuring OIDC authentication"
  AUTH_CLIENT_ID=$(extract_value "global.auth.oidc.oidcClientId" "$SERVICE_CONFIG_FILE")
  AUTH_URL=$(extract_value "global.auth.oidc.oidcAuthority" "$SERVICE_CONFIG_FILE")
else
  echo "Auth not configured"
fi

# Template the environment file
sed "s|{{BASE_DOMAIN}}|${BASE_DOMAIN}|g" "$ENV_TEMPLATE" > "$ENV_OUTPUT"
sed -i "s|{{AUTH_CLIENT_ID}}|${AUTH_CLIENT_ID}|g" "$ENV_OUTPUT"
sed -i "s|{{INTERNAL_AUTH_URL}}|${AUTH_URL}|g" "$ENV_OUTPUT"
sed -i "s|{{AUTH_INSECURE_SKIP_VERIFY}}|${AUTH_INSECURE_SKIP_VERIFY}|g" "$ENV_OUTPUT"
sed -i "s|{{ORGANIZATIONS_ENABLED}}|$ORGANIZATIONS_ENABLED|g" "$ENV_OUTPUT"

# Wait for certificates
wait_for_files "$CERTS_SOURCE_PATH/server.crt" "$CERTS_SOURCE_PATH/server.key"

# Copy certificates to destination path
cp "$CERTS_SOURCE_PATH/server.crt" "$CERTS_DEST_PATH/server.crt"
cp "$CERTS_SOURCE_PATH/server.key" "$CERTS_DEST_PATH/server.key"

if [ -f "$CERTS_SOURCE_PATH/auth/ca.crt" ]; then
  echo "Using provided auth CA certificate"
  cp "$CERTS_SOURCE_PATH/auth/ca.crt" "$CERTS_DEST_PATH/ca_auth.crt"
fi

echo "Initialization complete"
