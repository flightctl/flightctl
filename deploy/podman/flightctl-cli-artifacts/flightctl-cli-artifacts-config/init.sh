#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-cli-artifacts configuration"

source "/utils/init_utils.sh"

NGINX_CONFIG_FILE="/config-source/nginx.conf"
NGINX_CONFIG_OUTPUT="/config-destination/nginx.conf"
CERTS_SOURCE_PATH="/certs-source"
CERTS_DEST_PATH="/certs-destination"
SERVICE_CONFIG_FILE="/config-source/service-config.yaml"

# Write nginx configuration files
#
# Base file contains listen directives for both IPv4 and IPv6
cp "$NGINX_CONFIG_FILE" "$NGINX_CONFIG_OUTPUT"
# Removes IPv6 listen directive for the IPv4 configuration
sed '/^\s*listen\s*\[::\]:8090/d' "$NGINX_CONFIG_OUTPUT" > "${NGINX_CONFIG_OUTPUT}.ipv4" && \
# Removes IPv4 listen directive for the IPv6 configuration
sed '/^\s*listen\s*8090 ssl/d' "$NGINX_CONFIG_OUTPUT" > "${NGINX_CONFIG_OUTPUT}.ipv6"

# Handle server certificates
#
# The CLI artifacts container runs as user 1001 by default,
# so we need to ensure that the server certificate and key files
# can be read by this user.
#
# Check for custom certificates first, fall back to default location
CUSTOM_SERVER_CERTS=$(extract_value "global.baseDomainTls.customServerCerts" "$SERVICE_CONFIG_FILE")

if [ "$CUSTOM_SERVER_CERTS" = "true" ]; then
  cp "$CERTS_SOURCE_PATH/custom/server.crt" "$CERTS_DEST_PATH/server.crt"
  cp "$CERTS_SOURCE_PATH/custom/server.key" "$CERTS_DEST_PATH/server.key"
else
  cp "$CERTS_SOURCE_PATH/server.crt" "$CERTS_DEST_PATH/server.crt"
  cp "$CERTS_SOURCE_PATH/server.key" "$CERTS_DEST_PATH/server.key"
fi

# Set appropriate permissions for nginx user (1001)
chown 1001:0 "$CERTS_DEST_PATH/server.crt"
chmod 0440 "$CERTS_DEST_PATH/server.crt"
chown 1001:0 "$CERTS_DEST_PATH/server.key"
chmod 0440 "$CERTS_DEST_PATH/server.key"

echo "Initialization complete"
