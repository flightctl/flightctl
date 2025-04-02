#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-api configuration"

# Define paths
VALUES_FILE="/values/values.yaml"
CONFIG_TEMPLATE="/config/config.yaml.template"
CONFIG_OUTPUT="/config/config.yaml"
CERTS_DIR="/certs"

# Check if values file exists
if [ ! -f "$VALUES_FILE" ]; then
  echo "Error: Values file not found at $VALUES_FILE"
  exit 1
fi

# Check if config template exists
if [ ! -f "$CONFIG_TEMPLATE" ]; then
  echo "Error: Config template not found at $CONFIG_TEMPLATE"
  exit 1
fi

# Extract values from values.yaml using YAML-aware parsing for nested structures
BASE_DOMAIN=$(grep -A10 'global:' "$VALUES_FILE" | grep 'baseDomain:' | awk '{print $2}')
SRV_CERT_FILE=$(grep -A10 'global:' "$VALUES_FILE" | grep 'srvCertFile:' | awk '{print $2}')
SRV_KEY_FILE=$(grep -A10 'global:' "$VALUES_FILE" | grep 'srvKeyFile:' | awk '{print $2}')

# Verify required values were found
if [ -z "$BASE_DOMAIN" ]; then
  echo "Error: Could not find baseDomain in values file"
  exit 1
fi

# Handle certificate paths
if [ -n "$SRV_CERT_FILE" ] && [ -n "$SRV_KEY_FILE" ]; then
  echo "Using provided certificates"
  # Template the configuration file with certificate paths
  # These are relative to the container's filesystem and should have been mounted by the ExecStartPre script
  cat "$CONFIG_TEMPLATE" | \
    sed 's|{{BASE_DOMAIN}}|'"$BASE_DOMAIN"'|g' | \
    sed "s|{{SRV_CERT_FILE}}|/root/.flightctl/certs/provided/server.crt|g" | \
    sed "s|{{SRV_KEY_FILE}}|/root/.flightctl/certs/provided/server.key|g" \
    > "$CONFIG_OUTPUT"
else
  echo "No certificates provided"
  # Template the configuration file with empty certificate paths
  cat "$CONFIG_TEMPLATE" | \
    sed "s|{{BASE_DOMAIN}}|'"$BASE_DOMAIN"'|g" | \
    sed "s|{{SRV_CERT_FILE}}||g" | \
    sed "s|{{SRV_KEY_FILE}}||g" \
    > "$CONFIG_OUTPUT"
fi

echo "Initialization complete"
