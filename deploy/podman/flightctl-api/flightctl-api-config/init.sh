#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-api configuration"

source "/utils/init_utils.sh"

# Define paths
CERTS_SOURCE_PATH="/certs"
CERTS_DEST_PATH="/root/.flightctl/certs"
SERVICE_CONFIG_FILE="/service-config.yaml"
CONFIG_TEMPLATE="/config-source/config.yaml.template"
CONFIG_OUTPUT="/config-destination/config.yaml"
ENV_TEMPLATE="/config-source/env.template"
ENV_OUTPUT="/config-destination/env"

# Check if service config file exists
if [ ! -f "$SERVICE_CONFIG_FILE" ]; then
  echo "Error: Service config file not found at $SERVICE_CONFIG_FILE"
  exit 1
fi

# Extract values
BASE_DOMAIN=$(extract_value "baseDomain" "$SERVICE_CONFIG_FILE")
SRV_CERT_FILE=""
SRV_KEY_FILE=""

# Extract auth-related values
AUTH_TYPE=$(extract_value "type" "$SERVICE_CONFIG_FILE")
INSECURE_SKIP_TLS_VERIFY=$(extract_value "insecureSkipTlsVerify" "$SERVICE_CONFIG_FILE")
AUTH_CA_CERT=""
AAP_API_URL=""
AAP_EXTERNAL_API_URL=""
FLIGHTCTL_DISABLE_AUTH=""

# Verify required values were found
if [ -z "$BASE_DOMAIN" ]; then
  echo "Error: Could not find baseDomain in service config file"
  exit 1
fi

# Process auth settings based on auth type
if [ "$AUTH_TYPE" == "aap" ]; then
  echo "Configuring AAP authentication"
  AAP_API_URL=$(extract_value "apiUrl" "$SERVICE_CONFIG_FILE")
  AAP_EXTERNAL_API_URL=$(extract_value "externalApiUrl" "$SERVICE_CONFIG_FILE")
else
  echo "Auth not configured"
  FLIGHTCTL_DISABLE_AUTH="true"
fi

# Set cert paths
# If there are no server certs provided, they will be generated
# The variables set are relative to the container's filesystem
if [ -f "$CERTS_SOURCE_PATH/server.crt" ]; then
  SRV_CERT_FILE="$CERTS_DEST_PATH/server.crt"
fi
if [ -f "$CERTS_SOURCE_PATH/server.key" ]; then
  SRV_KEY_FILE="$CERTS_DEST_PATH/server.key"
fi
if [ -f "$CERTS_SOURCE_PATH/auth/ca.crt" ]; then
  AUTH_CA_CERT="$CERTS_DEST_PATH/auth/ca.crt"
fi

# Template the configuration file
sed -e "s|{{BASE_DOMAIN}}|$BASE_DOMAIN|g" \
    -e "s|{{SRV_CERT_FILE}}|$SRV_CERT_FILE|g" \
    -e "s|{{SRV_KEY_FILE}}|$SRV_KEY_FILE|g" \
    -e "s|{{INSECURE_SKIP_TLS_VERIFY}}|$INSECURE_SKIP_TLS_VERIFY|g" \
    -e "s|{{AAP_API_URL}}|$AAP_API_URL|g" \
    -e "s|{{AAP_EXTERNAL_API_URL}}|$AAP_EXTERNAL_API_URL|g" \
    -e "s|{{AUTH_CA_CERT}}|$AUTH_CA_CERT|g" \
    "$CONFIG_TEMPLATE" > "$CONFIG_OUTPUT"

# Template the environment file
sed "s|{{FLIGHTCTL_DISABLE_AUTH}}|$FLIGHTCTL_DISABLE_AUTH|g" "$ENV_TEMPLATE" > "$ENV_OUTPUT"

echo "Initialization complete"
