#!/bin/bash
set -euo pipefail

# Waits until a PostgreSQL database is ready for connections.
# Reads credentials from mounted secret files at /run/secrets/db/ and optional flags: --timeout, --sleep, --connection-timeout.
# Optionally reads database configuration from a YAML file specified by SERVICE_CONFIG_PATH.

# read_secret_file reads a secret from a file, returning the trimmed contents.
# Usage: value=$(read_secret_file /path/to/file)
read_secret_file() {
    local file="$1"
    if [ -f "$file" ]; then
        tr -d '\n\r' < "$file"
    fi
}

# Set default values for DB_* variables, preferring mounted secret files.
# DB_USER_FILE / DB_PASSWORD_FILE point to the correct secret path for the user type.
: "${DB_USER_FILE:=/run/secrets/db/user}"
: "${DB_PASSWORD_FILE:=/run/secrets/db/userPassword}"
: "${DB_HOST:=}"
: "${DB_PORT:=}"
: "${DB_NAME:=}"
: "${DB_USER:=$(read_secret_file "$DB_USER_FILE")}"
: "${DB_PASSWORD:=$(read_secret_file "$DB_PASSWORD_FILE")}"
: "${DB_SSL_MODE:=}"
: "${DB_SSL_CERT:=}"
: "${DB_SSL_KEY:=}"
: "${DB_SSL_ROOT_CERT:=}"

# Initialize defaults
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-60}"
SLEEP_INTERVAL="${SLEEP_INTERVAL:-2}"
CONNECTION_TIMEOUT="${CONNECTION_TIMEOUT:-3}"

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --timeout=*) TIMEOUT_SECONDS="${1#*=}"; shift ;;
        --sleep=*) SLEEP_INTERVAL="${1#*=}"; shift ;;
        --connection-timeout=*) CONNECTION_TIMEOUT="${1#*=}"; shift ;;
        --timeout) TIMEOUT_SECONDS="$2"; shift 2 ;;
        --sleep) SLEEP_INTERVAL="$2"; shift 2 ;;
        --connection-timeout) CONNECTION_TIMEOUT="$2"; shift 2 ;;
        --help|-h)
            echo "Usage: $0 [--timeout=SECONDS] [--sleep=SECONDS] [--connection-timeout=SECONDS]"
            echo "Wait for PostgreSQL database to become ready"
            echo ""
            echo "Options:"
            echo "  --timeout=SECONDS       Maximum time to wait (default: 180)"
            echo "  --sleep=SECONDS         Sleep interval between attempts (default: 2)"
            echo "  --connection-timeout=SECONDS  Connection timeout per attempt (default: 3)"
            echo ""
            echo "Credentials (read from mounted secret files):"
            echo "  DB_USER_FILE - Path to file containing DB username (default: /run/secrets/db/user)"
            echo "  DB_PASSWORD_FILE - Path to file containing DB password (default: /run/secrets/db/userPassword)"
            echo ""
            echo "Configuration:"
            echo "  DB_HOST - Database hostname (optional, default: flightctl-db)"
            echo "  DB_PORT - Database port (optional, default: 5432)"
            echo "  DB_NAME - Database name (optional, default: flightctl)"
            echo "  DB_SSL_MODE, DB_SSL_CERT, DB_SSL_KEY, DB_SSL_ROOT_CERT - SSL configuration (optional)"
            echo "  SERVICE_CONFIG_PATH - Path to service config YAML file (optional)"
            exit 0 ;;
        --*) echo "Unknown option $1" >&2; echo "Use --help for usage information" >&2; exit 1 ;;
        *) echo "Unknown argument: $1" >&2; echo "Use --help for usage information" >&2; exit 1 ;;
    esac
done

# Validate arguments
if ! [[ "$TIMEOUT_SECONDS" =~ ^[0-9]+$ ]]; then
    echo "Error: TIMEOUT_SECONDS must be a positive integer, got: $TIMEOUT_SECONDS" >&2
    exit 1
fi

if ! [[ "$SLEEP_INTERVAL" =~ ^[0-9]+$ ]]; then
    echo "Error: SLEEP_INTERVAL must be a positive integer, got: $SLEEP_INTERVAL" >&2
    exit 1
fi

if ! [[ "$CONNECTION_TIMEOUT" =~ ^[0-9]+$ ]]; then
    echo "Error: CONNECTION_TIMEOUT must be a positive integer, got: $CONNECTION_TIMEOUT" >&2
    exit 1
fi

# Load database configuration from SERVICE_CONFIG_PATH if provided
if [ -n "${SERVICE_CONFIG_PATH:-}" ]; then
    if [ ! -f "$SERVICE_CONFIG_PATH" ]; then
        echo "Warning: SERVICE_CONFIG_PATH is set but file not found: $SERVICE_CONFIG_PATH" >&2
    elif ! command -v python3 &> /dev/null; then
        echo "Warning: python3 command not found, cannot read service config file" >&2
    else
        echo "Loading database configuration from: $SERVICE_CONFIG_PATH"

        db_block=$(sed -n '/^database:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_PATH")

        # Read database configuration from YAML
        yaml_host=$(echo "$db_block" | sed -n 's/^[[:space:]]*hostname:[[:space:]]*["'\'']*\([^"'\'']*\)["'\'']*/\1/p' | head -1)
        yaml_port=$(echo "$db_block" | sed -n 's/^[[:space:]]*port:[[:space:]]*["'\'']*\([^"'\'']*\)["'\'']*/\1/p' | head -1)
        yaml_name=$(echo "$db_block" | sed -n 's/^[[:space:]]*name:[[:space:]]*["'\'']*\([^"'\'']*\)["'\'']*/\1/p' | head -1)
        yaml_user=$(echo "$db_block" | sed -n 's/^[[:space:]]*user:[[:space:]]*["'\'']*\([^"'\'']*\)["'\'']*/\1/p' | head -1)

        [ -z "$DB_HOST" ] && [ -n "$yaml_host" ] && DB_HOST="$yaml_host"
        [ -z "$DB_PORT" ] && [ -n "$yaml_port" ] && DB_PORT="$yaml_port"
        [ -z "$DB_NAME" ] && [ -n "$yaml_name" ] && DB_NAME="$yaml_name"
        [ -z "$DB_USER" ] && [ -n "$yaml_user" ] && DB_USER="$yaml_user"

        # Read SSL configuration from YAML
        # Environment variables will take precedence if they are already set
        yaml_ssl_mode=$(echo "$db_block" | sed -n 's/^[[:space:]]*sslmode:[[:space:]]*["'\'']*\([^"'\'']*\)["'\'']*/\1/p' | head -1)
        yaml_ssl_cert=$(echo "$db_block" | sed -n 's/^[[:space:]]*sslcert:[[:space:]]*["'\'']*\([^"'\'']*\)["'\'']*/\1/p' | head -1)
        yaml_ssl_key=$(echo "$db_block" | sed -n 's/^[[:space:]]*sslkey:[[:space:]]*["'\'']*\([^"'\'']*\)["'\'']*/\1/p' | head -1)
        yaml_ssl_root_cert=$(echo "$db_block" | sed -n 's/^[[:space:]]*sslrootcert:[[:space:]]*["'\'']*\([^"'\'']*\)["'\'']*/\1/p' | head -1)

        # Use SSL config from file only if environment variables are not already set
        [ -z "$DB_SSL_MODE" ] && [ -n "$yaml_ssl_mode" ] && DB_SSL_MODE="$yaml_ssl_mode"
        [ -z "$DB_SSL_CERT" ] && [ -n "$yaml_ssl_cert" ] && DB_SSL_CERT="$yaml_ssl_cert"
        [ -z "$DB_SSL_KEY" ] && [ -n "$yaml_ssl_key" ] && DB_SSL_KEY="$yaml_ssl_key"
        [ -z "$DB_SSL_ROOT_CERT" ] && [ -n "$yaml_ssl_root_cert" ] && DB_SSL_ROOT_CERT="$yaml_ssl_root_cert"
    fi
fi


# Validate required credentials
: "${DB_USER:?DB_USER must be set (check DB_USER_FILE=${DB_USER_FILE} is mounted or set via SERVICE_CONFIG_PATH)}"
: "${DB_PASSWORD:?DB_PASSWORD must be set (check DB_PASSWORD_FILE=${DB_PASSWORD_FILE} is mounted)}"

# Log connection details
echo "Waiting for PostgreSQL database to be ready..."
echo "Connection details:"
echo "  Host: ${DB_HOST}"
echo "  Port: ${DB_PORT}"
echo "  Database: ${DB_NAME}"
echo "  User: ${DB_USER}"
echo "  Timeout: ${TIMEOUT_SECONDS} seconds"
echo "  Sleep interval: ${SLEEP_INTERVAL} seconds"
echo "  Connection timeout: ${CONNECTION_TIMEOUT} seconds"

# Log SSL configuration (non-sensitive info only)
if [ -n "${DB_SSL_MODE:-}" ]; then
    echo "SSL configuration:"
    echo "  SSL Mode: ${DB_SSL_MODE}"
    [ -n "${DB_SSL_CERT:-}" ] && echo "  SSL Certificate: configured"
    [ -n "${DB_SSL_KEY:-}" ] && echo "  SSL Key: configured"
    [ -n "${DB_SSL_ROOT_CERT:-}" ] && echo "  SSL Root Certificate: configured"
fi
echo ""

# Main wait loop
start_time=$(date +%s)
end_time=$((start_time + TIMEOUT_SECONDS))

while [[ $(date +%s) -lt $end_time ]]; do
    current_time=$(date +%s)
    elapsed_time=$((current_time - start_time))

    echo "Checking connection (elapsed: ${elapsed_time}s)..."

    set +e
    error_output=$(
      { timeout "${CONNECTION_TIMEOUT}" \
          env PGHOST="$DB_HOST" PGPORT="$DB_PORT" PGUSER="$DB_USER" \
              PGDATABASE="$DB_NAME" PGPASSWORD="$DB_PASSWORD" \
              ${DB_SSL_MODE:+PGSSLMODE="$DB_SSL_MODE"} \
              ${DB_SSL_CERT:+PGSSLCERT="$DB_SSL_CERT"} \
              ${DB_SSL_KEY:+PGSSLKEY="$DB_SSL_KEY"} \
              ${DB_SSL_ROOT_CERT:+PGSSLROOTCERT="$DB_SSL_ROOT_CERT"} \
          psql -tAq -c "SELECT 1" >/dev/null; } 2>&1
    )
    connection_result=$?
    set -e

    # Handle timeout case (exit code 124)
    if [ $connection_result -eq 124 ]; then
        error_output="Connection timeout after ${CONNECTION_TIMEOUT} seconds"
        connection_result=1
    fi

    if [ $connection_result -eq 0 ]; then
        echo "SUCCESS: Database is ready and accepting connections!"
        echo "Total wait time: ${elapsed_time} seconds"
        exit 0
    else
        echo "Connection failed: $error_output"
        echo "Database not ready, waiting ${SLEEP_INTERVAL} seconds..."
	echo ""
    fi

    sleep "${SLEEP_INTERVAL}"
done

# Timeout reached
final_time=$(date +%s)
total_elapsed=$((final_time - start_time))
echo ""
echo "ERROR: Database failed to become ready within ${TIMEOUT_SECONDS} seconds" >&2
echo "Total elapsed time: ${total_elapsed} seconds" >&2
echo "Last error: $error_output" >&2
exit 1
