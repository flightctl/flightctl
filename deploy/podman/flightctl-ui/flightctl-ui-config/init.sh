#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-ui configuration"

source "/utils/init_utils.sh"

# Mounted volumes in the container
CERTS_SOURCE_PATH="/certs-source"
CERTS_DEST_PATH="/certs-destination"

# Copy certificates to destination path
if [ -f "$CERTS_SOURCE_PATH/auth/ca.crt" ]; then
  echo "Using provided auth CA certificate"
  cp "$CERTS_SOURCE_PATH/auth/ca.crt" "$CERTS_DEST_PATH/ca_auth.crt"
fi

echo "Initialization complete"
