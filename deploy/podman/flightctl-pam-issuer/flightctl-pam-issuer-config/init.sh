#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-pam-issuer configuration"

source "/utils/init_utils.sh"

# Define paths
export CERTS_SOURCE_PATH="/certs"
CERTS_DEST_PATH="/root/.flightctl/certs"
export SERVICE_CONFIG_FILE="/service-config.yaml"
CONFIG_TEMPLATE="/config-source/config.yaml.template"
CONFIG_OUTPUT="/config-destination/config.yaml"
ENV_TEMPLATE="/config-source/env.template"
ENV_OUTPUT="/config-destination/env"

# Check if service config file exists
if [ ! -f "$SERVICE_CONFIG_FILE" ]; then
  echo "Error: Service config file not found at $SERVICE_CONFIG_FILE"
  exit 1
fi

# Extract values
BASE_DOMAIN=$(extract_value "global.baseDomain" "$SERVICE_CONFIG_FILE")

# Extract database configuration
DB_EXTERNAL=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*external:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)
if [ "$DB_EXTERNAL" == "enabled" ]; then
  echo "Configuring external database connection"
  DB_HOSTNAME=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*hostname:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)
  DB_PORT=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*port:[[:space:]]*\([^[:space:]]*\).*/\1/p' | head -1)
  DB_NAME=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*name:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)
  DB_USER=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*user:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)

  # Extract SSL configuration for external database
  DB_SSL_MODE=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*sslmode:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)
  DB_SSL_CERT=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*sslcert:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)
  DB_SSL_KEY=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*sslkey:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)
  DB_SSL_ROOT_CERT=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n 's/^[[:space:]]*sslrootcert:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)

  # Build SSL configuration block
  DB_SSL_CONFIG=""
  if [ -n "$DB_SSL_MODE" ]; then
    DB_SSL_CONFIG="${DB_SSL_CONFIG}$(cat <<EOF

  sslmode: $DB_SSL_MODE
EOF
)"
  fi
  if [ -n "$DB_SSL_CERT" ]; then
    DB_SSL_CONFIG="${DB_SSL_CONFIG}$(cat <<EOF

  sslcert: $DB_SSL_CERT
EOF
)"
  fi
  if [ -n "$DB_SSL_KEY" ]; then
    DB_SSL_CONFIG="${DB_SSL_CONFIG}$(cat <<EOF

  sslkey: $DB_SSL_KEY
EOF
)"
  fi
  if [ -n "$DB_SSL_ROOT_CERT" ]; then
    DB_SSL_CONFIG="${DB_SSL_CONFIG}$(cat <<EOF

  sslrootcert: $DB_SSL_ROOT_CERT
EOF
)"
  fi
else
  echo "Configuring internal database connection"
  DB_HOSTNAME="flightctl-db"
  DB_PORT="5432"
  DB_NAME="flightctl"
  DB_USER="admin"
  DB_SSL_CONFIG=""
fi

# Use defaults if values not found
DB_HOSTNAME=${DB_HOSTNAME:-flightctl-db}
DB_PORT=${DB_PORT:-5432}
DB_NAME=${DB_NAME:-flightctl}
DB_USER=${DB_USER:-admin}

# Extract PAM OIDC Issuer configuration
PAM_OIDC_ISSUER=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "issuer:" | head -1 | sed 's/.*issuer:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//' | sed 's/#.*$//')
PAM_OIDC_CLIENT_ID=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "clientId:" | head -1 | sed 's/.*clientId:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')
PAM_OIDC_CLIENT_SECRET=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "clientSecret:" | head -1 | sed 's/.*clientSecret:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')
PAM_OIDC_SCOPES=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "scopes:" | head -1 | sed 's/.*scopes:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')
PAM_OIDC_REDIRECT_URIS=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "redirectUris:" | head -1 | sed 's/.*redirectUris:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')
PAM_OIDC_SERVICE=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "pamService:" | head -1 | sed 's/.*pamService:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')

# Set defaults
PAM_OIDC_CLIENT_ID=${PAM_OIDC_CLIENT_ID:-flightctl-client}
PAM_OIDC_SERVICE=${PAM_OIDC_SERVICE:-flightctl}

# Set issuer URL - if not specified in config, use BASE_DOMAIN:8444/api/v1/auth
if [ -z "$PAM_OIDC_ISSUER" ]; then
  PAM_OIDC_ISSUER="https://${BASE_DOMAIN}:8444/api/v1/auth"
fi

# Default redirect URI if not specified
if [ -z "$PAM_OIDC_REDIRECT_URIS" ]; then
  PAM_OIDC_REDIRECT_URIS="https://${BASE_DOMAIN}:443/auth/callback"
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

# Verify required values were found
if [ -z "$BASE_DOMAIN" ]; then
  echo "Error: Could not find baseDomain in service config file"
  exit 1
fi

# Create SSL config replacement file if needed
SSL_CONFIG_FILE=$(mktemp)
if [ -n "$DB_SSL_CONFIG" ]; then
  echo "$DB_SSL_CONFIG" > "$SSL_CONFIG_FILE"
fi

# Template the configuration file
sed -e "s|{{BASE_DOMAIN}}|$BASE_DOMAIN|g" \
    -e "s|{{DB_HOSTNAME}}|$DB_HOSTNAME|g" \
    -e "s|{{DB_PORT}}|$DB_PORT|g" \
    -e "s|{{DB_NAME}}|$DB_NAME|g" \
    -e "s|{{DB_USER}}|$DB_USER|g" \
    -e "s|{{PAM_OIDC_ISSUER}}|$PAM_OIDC_ISSUER|g" \
    -e "s|{{PAM_OIDC_CLIENT_ID}}|$PAM_OIDC_CLIENT_ID|g" \
    -e "s|{{PAM_OIDC_CLIENT_SECRET}}|$PAM_OIDC_CLIENT_SECRET|g" \
    -e "s|{{PAM_OIDC_SCOPES}}|$PAM_OIDC_SCOPES_YAML|g" \
    -e "s|{{PAM_OIDC_REDIRECT_URIS}}|$PAM_OIDC_REDIRECT_URIS_YAML|g" \
    -e "s|{{PAM_OIDC_SERVICE}}|$PAM_OIDC_SERVICE|g" \
    "$CONFIG_TEMPLATE" > "$CONFIG_OUTPUT.tmp"

# Handle SSL config replacement using awk for multi-line support
awk -v ssl_config_file="$SSL_CONFIG_FILE" '
/{{DB_SSL_CONFIG}}/ {
    if ((getline ssl_config < ssl_config_file) > 0) {
        print ssl_config
        while ((getline ssl_config < ssl_config_file) > 0) {
            print ssl_config
        }
        close(ssl_config_file)
    }
    next
}
{ print }
' "$CONFIG_OUTPUT.tmp" > "$CONFIG_OUTPUT"

# Clean up temporary files
rm -f "$CONFIG_OUTPUT.tmp" "$SSL_CONFIG_FILE"

# Template the environment file
sed "$ENV_TEMPLATE" > "$ENV_OUTPUT"

echo "PAM issuer initialization complete"

