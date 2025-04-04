#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-api configuration"

# Define paths
VALUES_FILE="/values/values.yaml"
CONFIG_TEMPLATE="/config/config.yaml.template"
CONFIG_OUTPUT="/config/config.yaml"
ENV_TEMPLATE="/config/env.template"
ENV_FILE="/config/env"

# Function to extract a value from the YAML file
extract_value() {
  local key="$1"
  grep "$key:" "$VALUES_FILE" | awk '{print $2}'
}

# Check if values file exists
if [ ! -f "$VALUES_FILE" ]; then
  echo "Error: Values file not found at $VALUES_FILE"
  exit 1
fi

# Extract values
BASE_DOMAIN=$(extract_value "baseDomain")
SRV_CERT_FILE=""
SRV_KEY_FILE=""

# Extract auth-related values
AUTH_TYPE=$(extract_value "type")
INSECURE_SKIP_TLS_VERIFY=$(extract_value "insecureSkipTlsVerify")
AUTH_CA_CERT=""
AAP_API_URL=""
AAP_EXTERNAL_API_URL=""
FLIGHTCTL_DISABLE_AUTH=""

# Verify required values were found
if [ -z "$BASE_DOMAIN" ]; then
  echo "Error: Could not find baseDomain in values file"
  exit 1
fi

# Process auth settings based on auth type
if [ "$AUTH_TYPE" == "aap" ]; then
  echo "Configuring AAP authentication"
  AAP_API_URL=$(extract_value "apiUrl")
  AAP_EXTERNAL_API_URL=$(extract_value "externalApiUrl")
else
  echo "Auth not configured"
  FLIGHTCTL_DISABLE_AUTH="true"
fi

# Set cert paths
# These are relative to the container's filesystem and should have been mounted by the ExecStartPre script
if [ -n "$(extract_value "srvCertFile")" ]; then
  SRV_CERT_FILE="/root/.flightctl/certs/provided/server.crt"
fi
if [ -n "$(extract_value "srvKeyFile")" ]; then
  SRV_KEY_FILE="/root/.flightctl/certs/provided/server.key"
fi
if [ -n "$(extract_value "caCert")" ]; then
  AUTH_CA_CERT="/root/.flightctl/certs/provided/ca_auth.crt"
fi

# Template the configuration file
cat "$CONFIG_TEMPLATE" | \
  sed "s|{{BASE_DOMAIN}}|$BASE_DOMAIN|g" | \
  sed "s|{{SRV_CERT_FILE}}|$SRV_CERT_FILE|g" | \
  sed "s|{{SRV_KEY_FILE}}|$SRV_KEY_FILE|g" | \
  sed "s|{{INSECURE_SKIP_TLS_VERIFY}}|$INSECURE_SKIP_TLS_VERIFY|g" | \
  sed "s|{{AAP_API_URL}}|$AAP_API_URL|g" | \
  sed "s|{{AAP_EXTERNAL_API_URL}}|$AAP_EXTERNAL_API_URL|g" | \
  sed "s|{{AUTH_CA_CERT}}|$AUTH_CA_CERT|g" \
  > "$CONFIG_OUTPUT"

# Template the environment file
sed "s|{{FLIGHTCTL_DISABLE_AUTH}}|$FLIGHTCTL_DISABLE_AUTH|g" "$ENV_TEMPLATE" > "$ENV_FILE"

echo "Initialization complete"
