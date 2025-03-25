#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-ui configuration"

# Mounted volumes in the container
CERTS_SOURCE_PATH="/certs-source"
CERTS_DEST_PATH="/certs-destination"
VALUES_FILE="/values.yaml"
ENV_TEMPLATE="/config/env.template"

# ENV file to be used by the application
ENV_FILE="/config/env"

# Function to extract a value from the YAML file
extract_value() {
    local key="$1"
    sed -n -E "s/^[[:space:]]*${key}:[[:space:]]*[\"']?([^\"'#]+)[\"']?.*$/\1/p" "$VALUES_FILE"
}

# Check if values file exists
if [ ! -f "$VALUES_FILE" ]; then
  echo "Error: Values file not found at $VALUES_FILE"
  exit 1
fi

# Extract base values from values.yaml
BASE_DOMAIN=$(extract_value "baseDomain")

# Extract auth-related values
AUTH_TYPE=$(extract_value "type")
AUTH_INSECURE_SKIP_VERIFY=$(extract_value "insecureSkipTlsVerify")
AUTH_CLIENT_ID=""
AUTH_URL=""

# Verify required values were found
if [ -z "$BASE_DOMAIN" ]; then
  echo "Error: Could not find baseDomain in values file"
  exit 1
fi

# Process auth settings based on auth type
if [ "$AUTH_TYPE" == "aap" ]; then
  echo "Configuring AAP authentication"
  AUTH_CLIENT_ID=$(extract_value "oAuthApplicationClientId")
  AUTH_URL=$(extract_value "apiUrl")
else
  echo "Auth not configured"
fi

# Template the environment file
sed "s|{{BASE_DOMAIN}}|${BASE_DOMAIN}|g" "$ENV_TEMPLATE" > "$ENV_FILE"
sed -i "s|{{AUTH_CLIENT_ID}}|${AUTH_CLIENT_ID}|g" "$ENV_FILE"
sed -i "s|{{INTERNAL_AUTH_URL}}|${AUTH_URL}|g" "$ENV_FILE"
sed -i "s|{{AUTH_INSECURE_SKIP_VERIFY}}|${AUTH_INSECURE_SKIP_VERIFY}|g" "$ENV_FILE"

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
