#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-cli-artifacts configuration"

source "/utils/init_utils.sh"

NGINX_CONFIG_FILE="/config-source/nginx.conf"
NGINX_CONFIG_OUTPUT="/config-destination/nginx.conf"
CERTS_SOURCE_PATH="/certs-source"
CERTS_DEST_PATH="/certs-destination"

# Write nginx configuration files
#
# Base file contains listen directives for both IPv4 and IPv6
cp "$NGINX_CONFIG_FILE" "$NGINX_CONFIG_OUTPUT"
# Removes IPv6 listen directive for the IPv4 configuration
sed '/^\s*listen\s*\[::\]:8090/d' "$NGINX_CONFIG_OUTPUT" > "${NGINX_CONFIG_OUTPUT}.ipv4" && \
# Removes IPv4 listen directive for the IPv6 configuration
sed '/^\s*listen\s*8090 ssl/d' "$NGINX_CONFIG_OUTPUT" > "${NGINX_CONFIG_OUTPUT}.ipv6"

# Wait for certificates
wait_for_files "$CERTS_SOURCE_PATH/flightctl-cli-artifacts/server.crt" "$CERTS_SOURCE_PATH/flightctl-cli-artifacts/server.key"

# Handle server certificates
#
# The CLI artifacts container runs as user 1001 by default,
# so we need to ensure that the server certificate and key files
# can be read by this user.
if [ -f "$CERTS_SOURCE_PATH/flightctl-cli-artifacts/server.crt" ]; then
  cp "$CERTS_SOURCE_PATH/flightctl-cli-artifacts/server.crt" "$CERTS_DEST_PATH/server.crt"
  chown 1001:0 "$CERTS_DEST_PATH/server.crt"
  chmod 0440 "$CERTS_DEST_PATH/server.crt"
else
  echo "Error: Server certificate not found at $CERTS_SOURCE_PATH/flightctl-cli-artifacts/server.crt"
  exit 1
fi
if [ -f "$CERTS_SOURCE_PATH/flightctl-cli-artifacts/server.key" ]; then
  cp "$CERTS_SOURCE_PATH/flightctl-cli-artifacts/server.key" "$CERTS_DEST_PATH/server.key"
  chown 1001:0 "$CERTS_DEST_PATH/server.key"
  chmod 0440 "$CERTS_DEST_PATH/server.key"
else
  echo "Error: Server key not found at $CERTS_SOURCE_PATH/flightctl-cli-artifacts/server.key"
  exit 1
fi

echo "Initialization complete"
