#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-api configuration"

# Define paths
VALUES_FILE="/values/values.yaml"
CONFIG_TEMPLATE="/config/config.yaml.template"
CONFIG_OUTPUT="/config/config.yaml"
CERTS_DIR="/certs"
ENV_TEMPLATE="/config/env.template"
ENV_FILE="/config/env"

# Check if values file exists
if [ ! -f "$VALUES_FILE" ]; then
  echo "Error: Values file not found at $VALUES_FILE"
  exit 1
fi

# Extract values from values.yaml using YAML-aware parsing for nested structures
BASE_DOMAIN=$(grep -A10 'global:' "$VALUES_FILE" | grep 'baseDomain:' | awk '{print $2}')
SRV_CERT_FILE=$(grep -A10 'global:' "$VALUES_FILE" | grep 'srvCertFile:' | awk '{print $2}')
SRV_KEY_FILE=$(grep -A10 'global:' "$VALUES_FILE" | grep 'srvKeyFile:' | awk '{print $2}')

# Extract auth-related values
AUTH_TYPE=$(grep -A20 'global:' "$VALUES_FILE" | grep -A2 'auth:' | grep 'type:' | awk '{print $2}')
INSECURE_SKIP_TLS_VERIFY=$(grep -A20 'global:' "$VALUES_FILE" | grep 'insecureSkipTlsVerify:' | awk '{print $2}')
AAP_API_URL=""
AAP_EXTERNAL_API_URL=""
FLIGHTCTL_DISABLE_AUTH="false"

# Verify required values were found
if [ -z "$BASE_DOMAIN" ]; then
  echo "Error: Could not find baseDomain in values file"
  exit 1
fi

# Process auth settings based on auth type
if [ "$AUTH_TYPE" == "aap" ]; then
  echo "Configuring AAP authentication"
  AAP_API_URL=$(grep -A20 'global:' "$VALUES_FILE" | grep -A10 'aap:' | grep 'apiUrl:' | awk '{print $2}')
  AAP_EXTERNAL_API_URL=$(grep -A20 'global:' "$VALUES_FILE" | grep -A10 'aap:' | grep 'externalApiUrl:' | awk '{print $2}')
else
  echo "Auth not configured"
  FLIGHTCTL_DISABLE_AUTH="true"
fi

# Handle certificate paths
if [ -n "$SRV_CERT_FILE" ] && [ -n "$SRV_KEY_FILE" ]; then
  echo "Using provided certificates"
  # Template the configuration file with certificate paths
  # These are relative to the container's filesystem and should have been mounted by the ExecStartPre script
  cat "$CONFIG_TEMPLATE" | \
    sed "s|{{BASE_DOMAIN}}|$BASE_DOMAIN|g" | \
    sed "s|{{SRV_CERT_FILE}}|/root/.flightctl/certs/provided/server.crt|g" | \
    sed "s|{{SRV_KEY_FILE}}|/root/.flightctl/certs/provided/server.key|g" | \
    sed "s|{{INSECURE_SKIP_TLS_VERIFY}}|$INSECURE_SKIP_TLS_VERIFY|g" | \
    sed "s|{{AAP_API_URL}}|$AAP_API_URL|g" | \
    sed "s|{{AAP_EXTERNAL_API_URL}}|$AAP_EXTERNAL_API_URL|g" \
    > "$CONFIG_OUTPUT"
else
  echo "No certificates provided"
  # Template the configuration file with empty certificate paths
  cat "$CONFIG_TEMPLATE" | \
    sed "s|{{BASE_DOMAIN}}|$BASE_DOMAIN|g" | \
    sed "s|{{SRV_CERT_FILE}}||g" | \
    sed "s|{{SRV_KEY_FILE}}||g" | \
    sed "s|{{INSECURE_SKIP_TLS_VERIFY}}|$INSECURE_SKIP_TLS_VERIFY|g" | \
    sed "s|{{AAP_API_URL}}|$AAP_API_URL|g" | \
    sed "s|{{AAP_EXTERNAL_API_URL}}|$AAP_EXTERNAL_API_URL|g" \
    > "$CONFIG_OUTPUT"
fi

# Template the environment file
sed "s|{{FLIGHTCTL_DISABLE_AUTH}}|$FLIGHTCTL_DISABLE_AUTH|g" "$ENV_TEMPLATE" > "$ENV_FILE"

echo "Initialization complete"
