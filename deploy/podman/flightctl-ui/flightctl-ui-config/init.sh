#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-ui configuration"

source "/utils/init_utils.sh"

# Mounted volumes in the container
CERTS_SOURCE_PATH="/certs-source"
CERTS_DEST_PATH="/certs-destination"
SERVICE_CONFIG_FILE="/config-source/service-config.yaml"

CUSTOM_SERVER_CERTS=$(extract_value "global.baseDomainTls.customServerCerts" "$SERVICE_CONFIG_FILE")
if [ "$CUSTOM_SERVER_CERTS" = "true" ]; then
  cp "$CERTS_SOURCE_PATH/custom/server.crt" "$CERTS_DEST_PATH/server.crt"
  cp "$CERTS_SOURCE_PATH/custom/server.key" "$CERTS_DEST_PATH/server.key"
else
  cp "$CERTS_SOURCE_PATH/server.crt" "$CERTS_DEST_PATH/server.crt"
  cp "$CERTS_SOURCE_PATH/server.key" "$CERTS_DEST_PATH/server.key"
fi

# TODO - if the ca cert path is dynamic in the ui container, we can remove this init container
# and mount the cert directory with env vars set properly
CUSTOM_AUTH_CACERT=$(extract_value "global.auth.customCaCert" "$SERVICE_CONFIG_FILE")
IS_USING_PAM_ISSUER=$(extract_value "global.auth.pamOidcIssuer.enabled" "$SERVICE_CONFIG_FILE")
if [ "$CUSTOM_AUTH_CACERT" = "true" ]; then
  cp "$CERTS_SOURCE_PATH/custom/auth/ca.crt" "$CERTS_DEST_PATH/ca_auth.crt"
elif [ "$IS_USING_PAM_ISSUER" = "true" ]; then
  cp "$CERTS_SOURCE_PATH/ca.crt" "$CERTS_DEST_PATH/ca_auth.crt"
fi

echo "Initialization complete"
