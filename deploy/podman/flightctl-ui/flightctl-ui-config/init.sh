#!/usr/bin/env bash

set -e

echo "Initializing flightctl-ui configuration"

# Mounted volumes in the container
CERTS_SOURCE_PATH="/certs-source"
CERTS_DEST_PATH="/certs-destination"
VALUES_FILE="/values.yaml"
ENV_TEMPLATE="/config/env.template"

# ENV file to be used by the application
ENV_FILE="/config/env"

# Check if values file exists
if [ ! -f "$VALUES_FILE" ]; then
  echo "Error: Values file not found at $VALUES_FILE"
  exit 1
fi

# Extract values from values.yaml
BASE_DOMAIN=$(grep -A10 'global:' "$VALUES_FILE" | grep 'baseDomain:' | awk '{print $2}')
SRV_CERT_FILE=$(grep -A10 'global:' "$VALUES_FILE" | grep 'srvCertFile:' | awk '{print $2}')
SRV_KEY_FILE=$(grep -A10 'global:' "$VALUES_FILE" | grep 'srvKeyFile:' | awk '{print $2}')

# Create destination directory for certificates
mkdir -p "$CERTS_DEST_PATH/provided"

# Process template and set initial environment variable replacements
echo "Processing environment template..."
sed "s|{{BASE_DOMAIN}}|${BASE_DOMAIN}|g" "$ENV_TEMPLATE" > "$ENV_FILE"

# Handle certificate setup and update env file accordingly
if [ -n "$SRV_CERT_FILE" ] && [ -n "$SRV_KEY_FILE" ]; then
  echo "Found user provided certificate configuration"

  # Copy server certificates if they exist in source
  if [ -f "$CERTS_SOURCE_PATH/provided/server.crt" ] && [ -f "$CERTS_SOURCE_PATH/provided/server.key" ]; then
    cp "$CERTS_SOURCE_PATH/provided/server.crt" "$CERTS_DEST_PATH/provided/server.crt"
    cp "$CERTS_SOURCE_PATH/provided/server.key" "$CERTS_DEST_PATH/provided/server.key"

    # Update environment file with TLS variables
    sed -i "s|{{TLS_CERT}}|TLS_CERT=/app/certs/provided/server.crt|g" "$ENV_FILE"
    sed -i "s|{{TLS_KEY}}|TLS_KEY=/app/certs/provided/server.key|g" "$ENV_FILE"
    echo "Added TLS certificate configuration to environment"
  else
    echo "Error: Certificates configured in values.yaml but not found in source volume"
    exit 1
  fi
else
  echo "Using generated certificates"
  cp "$CERTS_SOURCE_PATH/server.crt" "$CERTS_DEST_PATH/server.crt"
  cp "$CERTS_SOURCE_PATH/server.key" "$CERTS_DEST_PATH/server.key"

  # Update environment file with TLS variables
  sed -i "s|{{TLS_CERT}}|TLS_CERT=/app/certs/server.crt|g" "$ENV_FILE"
  sed -i "s|{{TLS_KEY}}|TLS_KEY=/app/certs/server.key|g" "$ENV_FILE"
fi

echo "Initialization complete"
