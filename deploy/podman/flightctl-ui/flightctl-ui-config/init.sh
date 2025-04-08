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
BASE_DOMAIN=$(extract_value "baseDomain" "$SERVICE_CONFIG_FILE")

# Extract auth-related values
AUTH_TYPE=$(extract_value "type" "$SERVICE_CONFIG_FILE")
AUTH_INSECURE_SKIP_VERIFY=$(extract_value "insecureSkipTlsVerify" "$SERVICE_CONFIG_FILE")
AUTH_CLIENT_ID=""
AUTH_URL=""

# Verify required values were found
if [ -z "$BASE_DOMAIN" ]; then
  echo "Error: Could not find baseDomain in service config file"
  exit 1
fi

# Process auth settings based on auth type
if [ "$AUTH_TYPE" == "aap" ]; then
  echo "Configuring AAP authentication"
  AUTH_CLIENT_ID=$(extract_value "oAuthApplicationClientId" "$SERVICE_CONFIG_FILE")
  AUTH_URL=$(extract_value "apiUrl" "$SERVICE_CONFIG_FILE")
else
  echo "Auth not configured"
fi

# Template the environment file
sed "s|{{BASE_DOMAIN}}|${BASE_DOMAIN}|g" "$ENV_TEMPLATE" > "$ENV_OUTPUT"
sed -i "s|{{AUTH_CLIENT_ID}}|${AUTH_CLIENT_ID}|g" "$ENV_OUTPUT"
sed -i "s|{{INTERNAL_AUTH_URL}}|${AUTH_URL}|g" "$ENV_OUTPUT"
sed -i "s|{{AUTH_INSECURE_SKIP_VERIFY}}|${AUTH_INSECURE_SKIP_VERIFY}|g" "$ENV_OUTPUT"

# Handle server certificates
if [ -f "$CERTS_SOURCE_PATH/server.crt" ]; then
  cp "$CERTS_SOURCE_PATH/server.crt" "$CERTS_DEST_PATH/server.crt"
else
  echo "Warning: Server certificate not found at $CERTS_SOURCE_PATH/server.crt"
  exit 1
fi
if [ -f "$CERTS_SOURCE_PATH/server.key" ]; then
  cp "$CERTS_SOURCE_PATH/server.key" "$CERTS_DEST_PATH/server.key"
else
  echo "Warning: Server key not found at $CERTS_SOURCE_PATH/server.key"
  exit 1
fi

if [ -f "$CERTS_SOURCE_PATH/auth/ca.crt" ]; then
  echo "Using provided auth CA certificate"
  cp "$CERTS_SOURCE_PATH/auth/ca.crt" "$CERTS_DEST_PATH/ca_auth.crt"
fi

echo "Initialization complete"
