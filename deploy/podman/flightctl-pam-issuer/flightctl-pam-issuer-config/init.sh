#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-pam-issuer configuration"

source "/utils/init_utils.sh"

# Define paths
export SERVICE_CONFIG_FILE="/service-config.yaml"
CONFIG_TEMPLATE="/config-source/config.yaml.template"
CONFIG_OUTPUT="/config-destination/config.yaml"

# Check if service config file exists
if [ ! -f "$SERVICE_CONFIG_FILE" ]; then
  echo "Error: Service config file not found at $SERVICE_CONFIG_FILE"
  exit 1
fi

# Extract values
BASE_DOMAIN=$(extract_value "global.baseDomain" "$SERVICE_CONFIG_FILE")

# Auto-detect baseDomain if not set
if [ -z "$BASE_DOMAIN" ]; then
  BASE_DOMAIN="$(ip route get 1.1.1.1 | grep -oP 'src \K\S+')"
  if [ -z "$BASE_DOMAIN" ]; then
    echo "Error: Could not auto-detect baseDomain and it is not set in service config file"
    exit 1
  fi
  echo "Auto-detected base domain: ${BASE_DOMAIN}"
fi

# Extract PAM OIDC Issuer configuration
PAM_OIDC_ISSUER=$(extract_value "global.auth.pamOidcIssuer.issuer" "$SERVICE_CONFIG_FILE")
PAM_OIDC_CLIENT_ID=$(extract_value "global.auth.pamOidcIssuer.clientId" "$SERVICE_CONFIG_FILE")
PAM_OIDC_CLIENT_SECRET=$(extract_value "global.auth.pamOidcIssuer.clientSecret" "$SERVICE_CONFIG_FILE")
PAM_OIDC_SCOPES=$(extract_value "global.auth.pamOidcIssuer.scopes" "$SERVICE_CONFIG_FILE")
PAM_OIDC_REDIRECT_URIS=$(extract_value "global.auth.pamOidcIssuer.redirectUris" "$SERVICE_CONFIG_FILE")
PAM_OIDC_SERVICE=$(extract_value "global.auth.pamOidcIssuer.pamService" "$SERVICE_CONFIG_FILE")

# Set defaults
PAM_OIDC_CLIENT_ID=${PAM_OIDC_CLIENT_ID:-flightctl-client}
PAM_OIDC_SERVICE=${PAM_OIDC_SERVICE:-flightctl}

# Set issuer URL - if not specified in config, use BASE_DOMAIN:8444/api/v1/auth
if [ -z "$PAM_OIDC_ISSUER" ]; then
  PAM_OIDC_ISSUER="https://${BASE_DOMAIN}:8444/api/v1/auth"
fi

# Default redirect URI if not specified
if [ -z "$PAM_OIDC_REDIRECT_URIS" ]; then
  PAM_OIDC_REDIRECT_URIS="https://${BASE_DOMAIN}:443/callback"
fi

# Convert comma-separated list to YAML array format
if [ -n "$PAM_OIDC_SCOPES" ]; then
  PAM_OIDC_SCOPES_YAML=$(echo "$PAM_OIDC_SCOPES" | sed 's/,/", "/g' | sed 's/^/["/' | sed 's/$/"]/')
else
  PAM_OIDC_SCOPES_YAML='["openid", "profile", "email", "roles"]'
fi

if [ -n "$PAM_OIDC_REDIRECT_URIS" ]; then
  PAM_OIDC_REDIRECT_URIS_YAML=$(echo "$PAM_OIDC_REDIRECT_URIS" | sed 's/,/", "/g' | sed 's/^/["/' | sed 's/$/"]/')
else
  PAM_OIDC_REDIRECT_URIS_YAML='[]'
fi

# Template the configuration file
sed -e "s|{{BASE_DOMAIN}}|$BASE_DOMAIN|g" \
    -e "s|{{PAM_OIDC_ISSUER}}|$PAM_OIDC_ISSUER|g" \
    -e "s|{{PAM_OIDC_CLIENT_ID}}|$PAM_OIDC_CLIENT_ID|g" \
    -e "s|{{PAM_OIDC_CLIENT_SECRET}}|$PAM_OIDC_CLIENT_SECRET|g" \
    -e "s|{{PAM_OIDC_SCOPES}}|$PAM_OIDC_SCOPES_YAML|g" \
    -e "s|{{PAM_OIDC_REDIRECT_URIS}}|$PAM_OIDC_REDIRECT_URIS_YAML|g" \
    -e "s|{{PAM_OIDC_SERVICE}}|$PAM_OIDC_SERVICE|g" \
    "$CONFIG_TEMPLATE" > "$CONFIG_OUTPUT"

echo "PAM issuer initialization complete"

