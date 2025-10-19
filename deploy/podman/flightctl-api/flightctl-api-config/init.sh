#!/usr/bin/env bash

set -eo pipefail

echo "Initializing flightctl-api configuration"

source "/utils/init_utils.sh"
source "/config-source/create_aap_application.sh"

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
BASE_DOMAIN=$(extract_value "baseDomain" "$SERVICE_CONFIG_FILE")
SRV_CERT_FILE=""
SRV_KEY_FILE=""

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

  # For external database: use Podman secrets just like internal database
else
  echo "Configuring internal database connection"
  DB_HOSTNAME="flightctl-db"
  DB_PORT="5432"
  DB_NAME="flightctl"
  DB_USER="admin"

  # No SSL configuration for internal database
  DB_SSL_CONFIG=""

  # For internal database: password will come from Podman secret
fi

# Use defaults if values not found
DB_HOSTNAME=${DB_HOSTNAME:-flightctl-db}
DB_PORT=${DB_PORT:-5432}
DB_NAME=${DB_NAME:-flightctl}
DB_USER=${DB_USER:-admin}

# Extract auth-related values
AUTH_TYPE=$(extract_value "type" "$SERVICE_CONFIG_FILE")
INSECURE_SKIP_TLS_VERIFY=$(extract_value "insecureSkipTlsVerify" "$SERVICE_CONFIG_FILE")
AUTH_CA_CERT=""
AAP_API_URL=""
AAP_EXTERNAL_API_URL=""
FLIGHTCTL_DISABLE_AUTH=""

# Extract rate limit values (defaults if not configured)
# Extract from service.rateLimit section
RATE_LIMIT_REQUESTS=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[[:space:]]*[^[:space:]]/p' | sed -n 's/^[[:space:]]*requests:[[:space:]]*\([^[:space:]]*\).*/\1/p' | head -1)
RATE_LIMIT_WINDOW=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[[:space:]]*[^[:space:]]/p' | sed -n 's/^[[:space:]]*window:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)
RATE_LIMIT_AUTH_REQUESTS=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[[:space:]]*[^[:space:]]/p' | sed -n 's/^[[:space:]]*authRequests:[[:space:]]*\([^[:space:]]*\).*/\1/p' | head -1)
RATE_LIMIT_AUTH_WINDOW=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[[:space:]]*[^[:space:]]/p' | sed -n 's/^[[:space:]]*authWindow:[[:space:]]*[\"'"'"']*\([^\"'"'"'[:space:]]*\)[\"'"'"']*.*/\1/p' | head -1)

# Use defaults if not found
RATE_LIMIT_REQUESTS=${RATE_LIMIT_REQUESTS:-60}
RATE_LIMIT_WINDOW=${RATE_LIMIT_WINDOW:-1m}
RATE_LIMIT_AUTH_REQUESTS=${RATE_LIMIT_AUTH_REQUESTS:-10}
RATE_LIMIT_AUTH_WINDOW=${RATE_LIMIT_AUTH_WINDOW:-1h}

# Extract organizations enabled value (defaults to false if not configured)
ORGANIZATIONS_ENABLED=$(sed -n '/^global:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*organizations:/,/^[^[:space:]]/p' | sed -n '/^[[:space:]]*enabled:[[:space:]]*\([^[:space:]]*\).*/s//\1/p' | head -1)
ORGANIZATIONS_ENABLED=${ORGANIZATIONS_ENABLED:-false}

# Verify required values were found
if [ -z "$BASE_DOMAIN" ]; then
  echo "Error: Could not find baseDomain in service config file"
  exit 1
fi

AUTH_SED_CMDS=()

# Process auth settings based on auth type
if [ "$AUTH_TYPE" == "aap" ]; then
  echo "Configuring AAP authentication"
  AAP_API_URL=$(extract_value "apiUrl" "$SERVICE_CONFIG_FILE")
  AAP_EXTERNAL_API_URL=$(extract_value "externalApiUrl" "$SERVICE_CONFIG_FILE")
  AAP_OAUTH_TOKEN=$(extract_value "oAuthToken" "$SERVICE_CONFIG_FILE")
  AAP_CLIENT_ID=$(extract_value "oAuthApplicationClientId" "$SERVICE_CONFIG_FILE")

  AUTH_SED_CMDS=(
    -e "/{{if AAP}}/d"
    -e "/{{elseif OIDC}}/,/{{endif}}/d"
    -e "s|{{AAP_API_URL}}|$AAP_API_URL|g"
    -e "s|{{AAP_EXTERNAL_API_URL}}|$AAP_EXTERNAL_API_URL|g"
  )

  # If client id is not set and we have an oauth token, create a new oauth application
  if [ -z "$AAP_CLIENT_ID" ] && [ -n "$AAP_OAUTH_TOKEN" ]; then
    create_oauth_application "$AAP_OAUTH_TOKEN" "$BASE_DOMAIN" "$AAP_API_URL" "$INSECURE_SKIP_TLS_VERIFY"
  fi
elif [ "$AUTH_TYPE" == "oidc" ]; then
  echo "Configuring OIDC authentication"
  OIDC_URL=$(extract_value "oidcAuthority" "$SERVICE_CONFIG_FILE")
  OIDC_EXTERNAL_URL=$(extract_value "externalOidcAuthority" "$SERVICE_CONFIG_FILE")

  AUTH_SED_CMDS=(
    -e "/{{if AAP}}/,/{{elseif OIDC}}/d"
    -e "/{{endif}}/d"
    -e "s|{{OIDC_URL}}|$OIDC_URL|g"
    -e "s|{{OIDC_EXTERNAL_URL}}|$OIDC_EXTERNAL_URL|g"
  )
else
  echo "Auth not configured"
  FLIGHTCTL_DISABLE_AUTH="true"
  AUTH_SED_CMDS+=(
    -e "/{{if AAP}}/,/{{endif}}/d"
  )
fi


# Set cert paths
# If there are no server certs provided, they will be generated
# The variables set are relative to the container's filesystem
if [ -f "$CERTS_SOURCE_PATH/server.crt" ]; then
  SRV_CERT_FILE="$CERTS_DEST_PATH/server.crt"
fi
if [ -f "$CERTS_SOURCE_PATH/server.key" ]; then
  SRV_KEY_FILE="$CERTS_DEST_PATH/server.key"
fi
if [ -f "$CERTS_SOURCE_PATH/auth/ca.crt" ]; then
  AUTH_CA_CERT="$CERTS_DEST_PATH/auth/ca.crt"
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
    -e "s|{{SRV_CERT_FILE}}|$SRV_CERT_FILE|g" \
    -e "s|{{SRV_KEY_FILE}}|$SRV_KEY_FILE|g" \
    -e "s|{{INSECURE_SKIP_TLS_VERIFY}}|$INSECURE_SKIP_TLS_VERIFY|g" \
    -e "s|{{AUTH_CA_CERT}}|$AUTH_CA_CERT|g" \
    -e "s|{{RATE_LIMIT_REQUESTS}}|$RATE_LIMIT_REQUESTS|g" \
    -e "s|{{RATE_LIMIT_WINDOW}}|$RATE_LIMIT_WINDOW|g" \
    -e "s|{{RATE_LIMIT_AUTH_REQUESTS}}|$RATE_LIMIT_AUTH_REQUESTS|g" \
    -e "s|{{RATE_LIMIT_AUTH_WINDOW}}|$RATE_LIMIT_AUTH_WINDOW|g" \
    -e "s|{{ORGANIZATIONS_ENABLED}}|$ORGANIZATIONS_ENABLED|g" \
    "${AUTH_SED_CMDS[@]}" \
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
sed -e "s|{{FLIGHTCTL_DISABLE_AUTH}}|$FLIGHTCTL_DISABLE_AUTH|g" \
    -e "s|{{RATE_LIMIT_REQUESTS}}|$RATE_LIMIT_REQUESTS|g" \
    -e "s|{{RATE_LIMIT_WINDOW}}|$RATE_LIMIT_WINDOW|g" \
    -e "s|{{AUTH_RATE_LIMIT_REQUESTS}}|$RATE_LIMIT_AUTH_REQUESTS|g" \
    -e "s|{{AUTH_RATE_LIMIT_WINDOW}}|$RATE_LIMIT_AUTH_WINDOW|g" \
    "$ENV_TEMPLATE" > "$ENV_OUTPUT"

echo "Initialization complete"
