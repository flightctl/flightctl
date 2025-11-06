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
AUTH_TYPE=$(extract_value "global.auth.type" "$SERVICE_CONFIG_FILE" | head -1)

# Translate "builtin" to "oidc" for backwards compatibility
# builtin is legacy auth that uses OIDC with PAM issuer enabled
if [ "$AUTH_TYPE" == "builtin" ]; then
  echo "Auth type 'builtin' detected - translating to 'oidc' with PAM issuer enabled"
  AUTH_TYPE="oidc"
  # Force PAM issuer to be enabled for builtin auth
  FORCE_PAM_ISSUER_ENABLED="true"
fi

INSECURE_SKIP_TLS_VERIFY=$(extract_value "global.auth.insecureSkipTlsVerify" "$SERVICE_CONFIG_FILE" | head -1)
AUTH_CA_CERT=""
AAP_API_URL=""
AAP_EXTERNAL_API_URL=""
FLIGHTCTL_DISABLE_AUTH=""


# Extract rate limit values from service-config.yaml
RATE_LIMIT_ENABLED=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[^[:space:]]/p' | sed -n '/^[[:space:]]*enabled:[[:space:]]*\([^[:space:]]*\).*/s//\1/p' | head -1)
RATE_LIMIT_REQUESTS=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[^[:space:]]/p' | sed -n '/^[[:space:]]*requests:[[:space:]]*\([^[:space:]]*\).*/s//\1/p' | head -1)
RATE_LIMIT_WINDOW=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[^[:space:]]/p' | sed -n '/^[[:space:]]*window:[[:space:]]*\([^[:space:]]*\).*/s//\1/p' | head -1)
RATE_LIMIT_AUTH_REQUESTS=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[^[:space:]]/p' | sed -n '/^[[:space:]]*authRequests:[[:space:]]*\([^[:space:]]*\).*/s//\1/p' | head -1)
RATE_LIMIT_AUTH_WINDOW=$(sed -n '/^service:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_FILE" | sed -n '/^[[:space:]]*rateLimit:/,/^[^[:space:]]/p' | sed -n '/^[[:space:]]*authWindow:[[:space:]]*\([^[:space:]]*\).*/s//\1/p' | head -1)

# Set defaults if not configured
RATE_LIMIT_ENABLED=${RATE_LIMIT_ENABLED:-true}
RATE_LIMIT_REQUESTS=${RATE_LIMIT_REQUESTS:-300}
RATE_LIMIT_WINDOW=${RATE_LIMIT_WINDOW:-1m}
RATE_LIMIT_AUTH_REQUESTS=${RATE_LIMIT_AUTH_REQUESTS:-20}
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
  
  # Extract OIDC configuration from service-config.yaml (under global.auth.oidc)
  # Use grep to find values in the oidc section
  OIDC_CLIENT_ID=$(grep -A 20 "oidc:" "$SERVICE_CONFIG_FILE" | grep "clientId:" | head -1 | sed 's/.*clientId:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')
  OIDC_ENABLED=$(grep -A 20 "oidc:" "$SERVICE_CONFIG_FILE" | grep "enabled:" | head -1 | sed 's/.*enabled:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')
  OIDC_ISSUER=$(grep -A 20 "oidc:" "$SERVICE_CONFIG_FILE" | grep "issuer:" | head -1 | sed 's/.*issuer:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')
  OIDC_EXTERNAL_AUTHORITY=$(grep -A 20 "oidc:" "$SERVICE_CONFIG_FILE" | grep "externalOidcAuthority:" | head -1 | sed 's/.*externalOidcAuthority:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')
  OIDC_ORG_ASSIGNMENT_TYPE=$(grep -A 5 "organizationAssignment:" "$SERVICE_CONFIG_FILE" | grep "type:" | head -1 | sed 's/.*type:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')
  OIDC_ORG_NAME=$(grep -A 5 "organizationAssignment:" "$SERVICE_CONFIG_FILE" | grep "organizationName:" | head -1 | sed 's/.*organizationName:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')
  OIDC_USERNAME_CLAIM=$(grep -A 20 "oidc:" "$SERVICE_CONFIG_FILE" | grep "usernameClaim:" | head -1 | sed 's/.*usernameClaim:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')
  OIDC_ROLE_CLAIM=$(grep -A 20 "oidc:" "$SERVICE_CONFIG_FILE" | grep "roleClaim:" | head -1 | sed 's/.*roleClaim:[[:space:]]*\(.*\)/\1/' | sed 's/[[:space:]]*$//')

  # Set defaults if not found
  OIDC_CLIENT_ID=${OIDC_CLIENT_ID:-flightctl-client}
  OIDC_ENABLED=${OIDC_ENABLED:-true}
  OIDC_ISSUER=${OIDC_ISSUER:-${BASE_URL}}
  OIDC_EXTERNAL_AUTHORITY=${OIDC_EXTERNAL_AUTHORITY:-${BASE_URL}}
  OIDC_ORG_ASSIGNMENT_TYPE=${OIDC_ORG_ASSIGNMENT_TYPE:-static}
  OIDC_ORG_NAME=${OIDC_ORG_NAME:-default}
  OIDC_USERNAME_CLAIM=${OIDC_USERNAME_CLAIM:-preferred_username}
  OIDC_ROLE_CLAIM=${OIDC_ROLE_CLAIM:-groups}

  # Extract PAM OIDC Issuer configuration (under global.auth.pamOidcIssuer)
  # Use grep to find values in the pamOidcIssuer section, stripping comments
  PAM_OIDC_ISSUER_ENABLED=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "enabled:" | head -1 | sed 's/.*enabled:[[:space:]]*\(.*\)/\1/' | sed 's/#.*$//' | sed 's/[[:space:]]*$//')
  PAM_OIDC_ISSUER=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "issuer:" | head -1 | sed 's/.*issuer:[[:space:]]*\(.*\)/\1/' | sed 's/#.*$//' | sed 's/[[:space:]]*$//')
  PAM_OIDC_CLIENT_ID=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "clientId:" | head -1 | sed 's/.*clientId:[[:space:]]*\(.*\)/\1/' | sed 's/#.*$//' | sed 's/[[:space:]]*$//')
  PAM_OIDC_CLIENT_SECRET=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "clientSecret:" | head -1 | sed 's/.*clientSecret:[[:space:]]*\(.*\)/\1/' | sed 's/#.*$//' | sed 's/[[:space:]]*$//')
  PAM_OIDC_SCOPES=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "scopes:" | head -1 | sed 's/.*scopes:[[:space:]]*\(.*\)/\1/' | sed 's/#.*$//' | sed 's/[[:space:]]*$//')
  PAM_OIDC_REDIRECT_URIS=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "redirectUris:" | head -1 | sed 's/.*redirectUris:[[:space:]]*\(.*\)/\1/' | sed 's/#.*$//' | sed 's/[[:space:]]*$//')
  PAM_OIDC_SERVICE=$(grep -A 20 "pamOidcIssuer:" "$SERVICE_CONFIG_FILE" | grep "pamService:" | head -1 | sed 's/.*pamService:[[:space:]]*\(.*\)/\1/' | sed 's/#.*$//' | sed 's/[[:space:]]*$//')

  # Set defaults for PAM
  # If FORCE_PAM_ISSUER_ENABLED is set (from builtin auth), always enable PAM
  if [ "$FORCE_PAM_ISSUER_ENABLED" == "true" ]; then
    PAM_OIDC_ISSUER_ENABLED="true"
  else
    PAM_OIDC_ISSUER_ENABLED=${PAM_OIDC_ISSUER_ENABLED:-true}
  fi
  PAM_OIDC_ISSUER=${PAM_OIDC_ISSUER:-${BASE_URL}}
  PAM_OIDC_CLIENT_ID=${PAM_OIDC_CLIENT_ID:-flightctl-client}
  PAM_OIDC_SERVICE=${PAM_OIDC_SERVICE:-flightctl}
  
  # Set PAM OIDC issuer URL for API server to connect to
  # This is the URL where the PAM issuer service is accessible
  PAM_OIDC_ISSUER_URL=${PAM_OIDC_ISSUER:-"https://${BASE_DOMAIN}:8444/api/v1/auth"}
  
  # Default redirect URI if not specified
  if [ -z "$PAM_OIDC_REDIRECT_URIS" ]; then
    PAM_OIDC_REDIRECT_URIS="${BASE_URL}/auth/callback"
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
    -e "s|{{OIDC_ROLE_CLAIM}}|$OIDC_ROLE_CLAIM|g"
    -e "s|{{PAM_OIDC_ISSUER}}|$PAM_OIDC_ISSUER|g"
    -e "s|{{PAM_OIDC_CLIENT_ID}}|$PAM_OIDC_CLIENT_ID|g"
    -e "s|{{PAM_OIDC_CLIENT_SECRET}}|$PAM_OIDC_CLIENT_SECRET|g"
    -e "s|{{PAM_OIDC_SCOPES}}|$PAM_OIDC_SCOPES_YAML|g"
    -e "s|{{PAM_OIDC_REDIRECT_URIS}}|$PAM_OIDC_REDIRECT_URIS_YAML|g"
    -e "s|{{PAM_OIDC_SERVICE}}|$PAM_OIDC_SERVICE|g"
    -e "s|{{PAM_OIDC_ISSUER_URL}}|$PAM_OIDC_ISSUER_URL|g"
  )
  
  # Handle PAM conditional block
  if [ "$PAM_OIDC_ISSUER_ENABLED" == "true" ]; then
    # PAM is enabled: remove the conditional markers but keep the content
    AUTH_SED_CMDS+=(
      -e "/{{if PAM_OIDC_ISSUER_ENABLED}}/d"
      -e "/{{endif PAM_OIDC_ISSUER_ENABLED}}/d"
    )
  else
    # PAM is disabled: remove the entire PAM block
    AUTH_SED_CMDS+=(
      -e "/{{if PAM_OIDC_ISSUER_ENABLED}}/,/{{endif PAM_OIDC_ISSUER_ENABLED}}/d"
    )
  fi
else
  echo "Auth not configured"
  FLIGHTCTL_DISABLE_AUTH="true"
  AUTH_SED_CMDS=(
    -e "/{{if AAP}}/,/{{endif AAP}}/d"
    -e "/{{if OIDC}}/,/{{endif OIDC}}/d"
    -e "/{{if PAM_OIDC_ISSUER_ENABLED}}/,/{{endif PAM_OIDC_ISSUER_ENABLED}}/d"
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
    -e "s|{{RATE_LIMIT_ENABLED}}|$RATE_LIMIT_ENABLED|g" \
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
    "$ENV_TEMPLATE" > "$ENV_OUTPUT"

echo "Initialization complete"
