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
BASE_DOMAIN=$(extract_value "global.baseDomain" "$SERVICE_CONFIG_FILE")
BASE_URL="https://${BASE_DOMAIN}"
SRV_CERT_FILE=""
SRV_KEY_FILE=""

# Extract database configuration
DB_EXTERNAL=$(extract_value "db.external" "$SERVICE_CONFIG_FILE")
if [ "$DB_EXTERNAL" == "enabled" ]; then
  echo "Configuring external database connection"
  DB_HOSTNAME=$(extract_value "db.hostname" "$SERVICE_CONFIG_FILE")
  DB_PORT=$(extract_value "db.port" "$SERVICE_CONFIG_FILE")
  DB_NAME=$(extract_value "db.name" "$SERVICE_CONFIG_FILE")
  DB_USER=$(extract_value "db.user" "$SERVICE_CONFIG_FILE")

  # Extract SSL configuration for external database
  DB_SSL_MODE=$(extract_value "db.sslmode" "$SERVICE_CONFIG_FILE")
  DB_SSL_CERT=$(extract_value "db.sslcert" "$SERVICE_CONFIG_FILE")
  DB_SSL_KEY=$(extract_value "db.sslkey" "$SERVICE_CONFIG_FILE")
  DB_SSL_ROOT_CERT=$(extract_value "db.sslrootcert" "$SERVICE_CONFIG_FILE")

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
echo "Extracting auth type from service config..."
AUTH_TYPE=$(extract_value "global.auth.type" "$SERVICE_CONFIG_FILE" | head -1)
echo "Extracted AUTH_TYPE='$AUTH_TYPE'"

# Translate "builtin" to "oidc" for backwards compatibility
# builtin is legacy auth that uses OIDC with PAM issuer enabled
if [ "$AUTH_TYPE" == "builtin" ]; then
  echo "Auth type 'builtin' detected - translating to 'oidc' with PAM issuer enabled"
  AUTH_TYPE="oidc"
  # Force PAM issuer to be enabled for builtin auth
  FORCE_PAM_ISSUER_ENABLED="true"
fi
echo "Final AUTH_TYPE after processing='$AUTH_TYPE'"

INSECURE_SKIP_TLS_VERIFY=$(extract_value "global.auth.insecureSkipTlsVerify" "$SERVICE_CONFIG_FILE" | head -1)
AUTH_CA_CERT=""
AAP_API_URL=""
AAP_EXTERNAL_API_URL=""
FLIGHTCTL_DISABLE_AUTH=""


# Extract rate limit values from service-config.yaml
RATE_LIMIT_ENABLED=$(extract_value "service.rateLimit.enabled" "$SERVICE_CONFIG_FILE")
RATE_LIMIT_REQUESTS=$(extract_value "service.rateLimit.requests" "$SERVICE_CONFIG_FILE")
RATE_LIMIT_WINDOW=$(extract_value "service.rateLimit.window" "$SERVICE_CONFIG_FILE")
RATE_LIMIT_AUTH_REQUESTS=$(extract_value "service.rateLimit.authRequests" "$SERVICE_CONFIG_FILE")
RATE_LIMIT_AUTH_WINDOW=$(extract_value "service.rateLimit.authWindow" "$SERVICE_CONFIG_FILE")

# Set defaults if not configured
RATE_LIMIT_ENABLED=${RATE_LIMIT_ENABLED:-true}
RATE_LIMIT_REQUESTS=${RATE_LIMIT_REQUESTS:-300}
RATE_LIMIT_WINDOW=${RATE_LIMIT_WINDOW:-1m}
RATE_LIMIT_AUTH_REQUESTS=${RATE_LIMIT_AUTH_REQUESTS:-20}
RATE_LIMIT_AUTH_WINDOW=${RATE_LIMIT_AUTH_WINDOW:-1h}

# Extract organizations enabled value (defaults to false if not configured)
ORGANIZATIONS_ENABLED=$(extract_value "global.organizations.enabled" "$SERVICE_CONFIG_FILE")
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
  AAP_API_URL=$(extract_value "global.auth.aap.apiUrl" "$SERVICE_CONFIG_FILE")
  AAP_EXTERNAL_API_URL=$(extract_value "global.auth.aap.externalApiUrl" "$SERVICE_CONFIG_FILE")
  AAP_OAUTH_TOKEN=$(extract_value "global.auth.aap.oAuthToken" "$SERVICE_CONFIG_FILE")
  AAP_CLIENT_ID=$(extract_value "global.auth.aap.oAuthApplicationClientId" "$SERVICE_CONFIG_FILE")

  AUTH_SED_CMDS=(
    -e "/{{if AAP}}/d"
    -e "/{{endif AAP}}/d"
    -e "s|{{AAP_API_URL}}|$AAP_API_URL|g"
    -e "s|{{AAP_EXTERNAL_API_URL}}|$AAP_EXTERNAL_API_URL|g"
    -e "/{{if OIDC}}/,/{{endif OIDC}}/d"
    -e "/{{if PAM_OIDC_ISSUER_ENABLED}}/,/{{endif PAM_OIDC_ISSUER_ENABLED}}/d"
  )

  # If client id is not set and we have an oauth token, create a new oauth application
  if [ -z "$AAP_CLIENT_ID" ] && [ -n "$AAP_OAUTH_TOKEN" ]; then
    create_oauth_application "$AAP_OAUTH_TOKEN" "$BASE_DOMAIN" "$AAP_API_URL" "$INSECURE_SKIP_TLS_VERIFY"
  fi
elif [ "$AUTH_TYPE" == "oidc" ]; then
  echo "Configuring OIDC authentication"
  
  echo "Extracting OIDC configuration values..."
  # Extract OIDC configuration from service-config.yaml (under global.auth.oidc)
  OIDC_CLIENT_ID=$(extract_value "global.auth.oidc.oidcClientId" "$SERVICE_CONFIG_FILE")
  OIDC_ISSUER=$(extract_value "global.auth.oidc.oidcAuthority" "$SERVICE_CONFIG_FILE")
  OIDC_EXTERNAL_AUTHORITY=$(extract_value "global.auth.oidc.externalOidcAuthority" "$SERVICE_CONFIG_FILE")
  
  # These fields may not exist in current config but keep for compatibility
  OIDC_ENABLED=$(extract_value "global.auth.oidc.enabled" "$SERVICE_CONFIG_FILE")
  OIDC_ORG_ASSIGNMENT_TYPE=$(extract_value "global.auth.oidc.organizationAssignment.type" "$SERVICE_CONFIG_FILE")
  OIDC_ORG_NAME=$(extract_value "global.auth.oidc.organizationAssignment.organizationName" "$SERVICE_CONFIG_FILE")
  OIDC_USERNAME_CLAIM=$(extract_value "global.auth.oidc.usernameClaim" "$SERVICE_CONFIG_FILE")
  OIDC_ROLE_ASSIGNMENT_TYPE=$(extract_value "global.auth.oidc.roleAssignment.type" "$SERVICE_CONFIG_FILE")
  OIDC_ROLE_ASSIGNMENT_CLAIM_PATH=$(extract_value "global.auth.oidc.roleAssignment.claimPath" "$SERVICE_CONFIG_FILE")

  echo "Extracting PAM OIDC Issuer configuration..."
  # Extract PAM OIDC Issuer configuration (under global.auth.pamOidcIssuer)
  PAM_OIDC_ISSUER_ENABLED=$(extract_value "global.auth.pamOidcIssuer.enabled" "$SERVICE_CONFIG_FILE")
  PAM_OIDC_ISSUER=$(extract_value "global.auth.pamOidcIssuer.issuer" "$SERVICE_CONFIG_FILE")
  PAM_OIDC_CLIENT_ID=$(extract_value "global.auth.pamOidcIssuer.clientId" "$SERVICE_CONFIG_FILE")
  PAM_OIDC_CLIENT_SECRET=$(extract_value "global.auth.pamOidcIssuer.clientSecret" "$SERVICE_CONFIG_FILE")
  PAM_OIDC_SCOPES=$(extract_value "global.auth.pamOidcIssuer.scopes" "$SERVICE_CONFIG_FILE")
  PAM_OIDC_REDIRECT_URIS=$(extract_value "global.auth.pamOidcIssuer.redirectUris" "$SERVICE_CONFIG_FILE")
  PAM_OIDC_SERVICE=$(extract_value "global.auth.pamOidcIssuer.pamService" "$SERVICE_CONFIG_FILE")

  echo "Setting PAM defaults..."
  # Set defaults for PAM
  # If FORCE_PAM_ISSUER_ENABLED is set (from builtin auth), always enable PAM
  if [ "$FORCE_PAM_ISSUER_ENABLED" == "true" ]; then
    PAM_OIDC_ISSUER_ENABLED="true"
  else
    PAM_OIDC_ISSUER_ENABLED=${PAM_OIDC_ISSUER_ENABLED:-true}
  fi
  PAM_OIDC_CLIENT_ID=${PAM_OIDC_CLIENT_ID:-flightctl-client}
  PAM_OIDC_SERVICE=${PAM_OIDC_SERVICE:-flightctl}
  
  echo "Setting PAM OIDC issuer URL..."
  # Set PAM OIDC issuer URL for API server to connect to
  # This is the URL where the PAM issuer service is accessible
  # Default to port 8444 with /api/v1/auth path if not specified
  if [ -z "$PAM_OIDC_ISSUER" ]; then
    PAM_OIDC_ISSUER_URL="https://${BASE_DOMAIN}:8444/api/v1/auth"
  else
    PAM_OIDC_ISSUER_URL="$PAM_OIDC_ISSUER"
  fi

  echo "Setting OIDC defaults..."
  # Set defaults if not found
  OIDC_CLIENT_ID=${OIDC_CLIENT_ID:-flightctl-client}
  OIDC_ENABLED=${OIDC_ENABLED:-true}
  OIDC_ORG_ASSIGNMENT_TYPE=${OIDC_ORG_ASSIGNMENT_TYPE:-static}
  OIDC_ORG_NAME=${OIDC_ORG_NAME:-default}
  OIDC_USERNAME_CLAIM=${OIDC_USERNAME_CLAIM:-preferred_username}
  OIDC_ROLE_ASSIGNMENT_TYPE=${OIDC_ROLE_ASSIGNMENT_TYPE:-dynamic}
  OIDC_ROLE_ASSIGNMENT_CLAIM_PATH=${OIDC_ROLE_ASSIGNMENT_CLAIM_PATH:-groups}
  
  # When PAM issuer is enabled, OIDC authority should point to PAM issuer (port 8444)
  if [ "$PAM_OIDC_ISSUER_ENABLED" == "true" ]; then
    # Force OIDC issuer to use PAM issuer URL when PAM is enabled
    OIDC_ISSUER="$PAM_OIDC_ISSUER_URL"
    OIDC_EXTERNAL_AUTHORITY="${OIDC_EXTERNAL_AUTHORITY:-$PAM_OIDC_ISSUER_URL}"
  else
    OIDC_ISSUER=${OIDC_ISSUER:-${BASE_URL}}
    OIDC_EXTERNAL_AUTHORITY=${OIDC_EXTERNAL_AUTHORITY:-${BASE_URL}}
  fi
  
  echo "Building sed commands for OIDC configuration..."
  # Build sed commands for OIDC
  AUTH_SED_CMDS=(
    -e "/{{if AAP}}/,/{{endif AAP}}/d"
    -e "/{{if OIDC}}/d"
    -e "/{{endif OIDC}}/d"
    -e "s|{{OIDC_CLIENT_ID}}|$OIDC_CLIENT_ID|g"
    -e "s|{{OIDC_ENABLED}}|$OIDC_ENABLED|g"
    -e "s|{{OIDC_ISSUER}}|$OIDC_ISSUER|g"
    -e "s|{{OIDC_EXTERNAL_AUTHORITY}}|$OIDC_EXTERNAL_AUTHORITY|g"
    -e "s|{{OIDC_ORG_ASSIGNMENT_TYPE}}|$OIDC_ORG_ASSIGNMENT_TYPE|g"
    -e "s|{{OIDC_ORG_NAME}}|$OIDC_ORG_NAME|g"
    -e "s|{{OIDC_USERNAME_CLAIM}}|$OIDC_USERNAME_CLAIM|g"
    -e "s|{{OIDC_ROLE_ASSIGNMENT_TYPE}}|$OIDC_ROLE_ASSIGNMENT_TYPE|g"
    -e "s|{{OIDC_ROLE_ASSIGNMENT_CLAIM_PATH}}|$OIDC_ROLE_ASSIGNMENT_CLAIM_PATH|g"
  )
  
  echo "OIDC configuration complete"
else
  echo "Auth not configured"
  FLIGHTCTL_DISABLE_AUTH="true"
  AUTH_SED_CMDS=(
    -e "/{{if AAP}}/,/{{endif AAP}}/d"
    -e "/{{if OIDC}}/,/{{endif OIDC}}/d"
  )
fi

echo "Auth configuration complete, setting up certificates..."

# Set cert paths
# If there are no server certs provided, they will be generated
# The variables set are relative to the container's filesystem
if [ -f "$CERTS_SOURCE_PATH/server.crt" ]; then
  SRV_CERT_FILE="$CERTS_DEST_PATH/server.crt"
  echo "Found server certificate at $CERTS_SOURCE_PATH/server.crt"
fi
if [ -f "$CERTS_SOURCE_PATH/server.key" ]; then
  SRV_KEY_FILE="$CERTS_DEST_PATH/server.key"
  echo "Found server key at $CERTS_SOURCE_PATH/server.key"
fi
if [ -f "$CERTS_SOURCE_PATH/auth/ca.crt" ]; then
  AUTH_CA_CERT="$CERTS_DEST_PATH/auth/ca.crt"
  echo "Found auth CA cert at $CERTS_SOURCE_PATH/auth/ca.crt"
fi

echo "Creating SSL config file..."
# Create SSL config replacement file if needed
SSL_CONFIG_FILE=$(mktemp)
if [ -n "$DB_SSL_CONFIG" ]; then
  echo "$DB_SSL_CONFIG" > "$SSL_CONFIG_FILE"
fi

echo "Templating configuration file from $CONFIG_TEMPLATE to $CONFIG_OUTPUT..."
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
    -e "s|{{RATE_LIMIT_ENABLED}}|$RATE_LIMIT_ENABLED|g" \
    -e "s|{{RATE_LIMIT_REQUESTS}}|$RATE_LIMIT_REQUESTS|g" \
    -e "s|{{RATE_LIMIT_WINDOW}}|$RATE_LIMIT_WINDOW|g" \
    -e "s|{{RATE_LIMIT_AUTH_REQUESTS}}|$RATE_LIMIT_AUTH_REQUESTS|g" \
    -e "s|{{RATE_LIMIT_AUTH_WINDOW}}|$RATE_LIMIT_AUTH_WINDOW|g" \
    -e "s|{{ORGANIZATIONS_ENABLED}}|$ORGANIZATIONS_ENABLED|g" \
    "${AUTH_SED_CMDS[@]}" \
    "$CONFIG_TEMPLATE" > "$CONFIG_OUTPUT.tmp"

echo "Processing SSL config replacement..."
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

echo "Configuration file created successfully"

# Clean up temporary files
rm -f "$CONFIG_OUTPUT.tmp" "$SSL_CONFIG_FILE"
echo "Temporary files cleaned up"

echo "Templating environment file from $ENV_TEMPLATE to $ENV_OUTPUT..."
# Template the environment file
sed -e "s|{{FLIGHTCTL_DISABLE_AUTH}}|$FLIGHTCTL_DISABLE_AUTH|g" \
    "$ENV_TEMPLATE" > "$ENV_OUTPUT"

echo "Environment file created successfully"
echo "Initialization complete"
