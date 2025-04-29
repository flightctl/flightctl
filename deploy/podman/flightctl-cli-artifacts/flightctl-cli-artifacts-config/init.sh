#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-cli-artifacts configuration"

source "/utils/init_utils.sh"

SERVICE_CONFIG_FILE="/service-config.yaml"
ENV_TEMPLATE="/config-source/env.template"
ENV_OUTPUT="/config-destination/env"
NGINX_CONFIG_FILE="/config-source/nginx.conf"
NGINX_CONFIG_OUTPUT="/config-destination/nginx.conf"

BASE_DOMAIN=$(extract_value "baseDomain" "$SERVICE_CONFIG_FILE")

# Verify baseDomain was found
if [ -z "$BASE_DOMAIN" ]; then
  echo "Error: Could not find baseDomain in service config file"
  exit 1
fi

# Template the environment file
sed "s|{{BASE_DOMAIN}}|$BASE_DOMAIN|g" "$ENV_TEMPLATE" > "$ENV_OUTPUT"

# Write nginx configuration files
#
# Base file contains listen directives for both IPv4 and IPv6
cp "$NGINX_CONFIG_FILE" "$NGINX_CONFIG_OUTPUT"
# Removes IPv6 listen directive for the IPv4 configuration
sed '/^\s*listen\s*\[::\]:443/d' "$NGINX_CONFIG_OUTPUT" > "${NGINX_CONFIG_OUTPUT}.ipv4" && \
# Removes IPv4 listen directive for the IPv6 configuration
sed '/^\s*listen\s*443 ssl/d' "$NGINX_CONFIG_OUTPUT" > "${NGINX_CONFIG_OUTPUT}.ipv6"

echo "Initialization complete"
