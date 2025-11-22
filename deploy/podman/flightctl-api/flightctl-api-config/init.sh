#!/usr/bin/env bash

set -eo pipefail

echo "Initialization for flightctl-api"

source "/utils/init_utils.sh"
source "/config-source/create_aap_application.sh"

# Define paths
export SERVICE_CONFIG_FILE="/service-config.yaml"

# Check if service config file exists
if [ ! -f "$SERVICE_CONFIG_FILE" ]; then
  echo "Error: Service config file not found at $SERVICE_CONFIG_FILE"
  exit 1
fi

# Extract auth-related values
AUTH_TYPE=$(extract_value "global.auth.type" "$SERVICE_CONFIG_FILE")
INSECURE_SKIP_TLS_VERIFY=$(extract_value "global.auth.insecureSkipTlsVerify" "$SERVICE_CONFIG_FILE")

# Process auth settings based on auth type
if [ "$AUTH_TYPE" == "aap" ]; then
  echo "Configuring AAP authentication"
  BASE_DOMAIN=$(extract_value "global.baseDomain" "$SERVICE_CONFIG_FILE")
  AAP_API_URL=$(extract_value "global.auth.aap.apiUrl" "$SERVICE_CONFIG_FILE")
  AAP_OAUTH_TOKEN=$(extract_value "global.auth.aap.oAuthToken" "$SERVICE_CONFIG_FILE")
  AAP_CLIENT_ID=$(extract_value "global.auth.aap.oAuthApplicationClientId" "$SERVICE_CONFIG_FILE")

  # If client id is not set and we have an oauth token, create a new oauth application
  if [ -z "$AAP_CLIENT_ID" ] && [ -n "$AAP_OAUTH_TOKEN" ]; then
    create_oauth_application "$AAP_OAUTH_TOKEN" "$BASE_DOMAIN" "$AAP_API_URL" "$INSECURE_SKIP_TLS_VERIFY"
  fi
fi

echo "Initialization complete"
