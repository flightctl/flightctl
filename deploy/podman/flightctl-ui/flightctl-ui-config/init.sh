#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-ui configuration"

source "/utils/init_utils.sh"

# Mounted volumes in the container
CERTS_SOURCE_PATH="/certs-source"
CERTS_DEST_PATH="/certs-destination"

# Wait for certificates
wait_for_files "$CERTS_SOURCE_PATH/flightctl-ui/server.crt" "$CERTS_SOURCE_PATH/flightctl-ui/server.key"

# Copy certificates to destination path
cp "$CERTS_SOURCE_PATH/flightctl-ui/server.crt" "$CERTS_DEST_PATH/server.crt"
cp "$CERTS_SOURCE_PATH/flightctl-ui/server.key" "$CERTS_DEST_PATH/server.key"
if [ -f "$CERTS_SOURCE_PATH/auth/ca.crt" ]; then
  echo "Using provided auth CA certificate"
  cp "$CERTS_SOURCE_PATH/auth/ca.crt" "$CERTS_DEST_PATH/ca_auth.crt"
fi

echo "Initialization complete"
