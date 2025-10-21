#!/bin/bash
set -euo pipefail

# Waits until a PostgreSQL database is ready for connections.
# Uses DB_* env vars (mapped to PG* internally) and optional flags: --timeout, --sleep, --connection-timeout.
# Optionally reads database configuration from a YAML file specified by SERVICE_CONFIG_PATH.
# Environment variables override values from the YAML file.

# Set default values for DB_* variables
: "${DB_HOST:=flightctl-db}"
: "${DB_PORT:=5432}"
: "${DB_NAME:=flightctl}"
: "${DB_USER:=}"
: "${DB_PASSWORD:=}"
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
            echo "Environment variables:"
            echo "  DB_USER, DB_PASSWORD - Database connection details (required)"
            echo "  DB_HOST - Database hostname (optional, default: flightctl-db)"
            echo "  DB_PORT - Database port (optional, default: 5432)"
            echo "  DB_NAME - Database name (optional, default: flightctl)"
            echo "  DB_SSL_MODE, DB_SSL_CERT, DB_SSL_KEY, DB_SSL_ROOT_CERT - SSL configuration (optional)"
            echo "  SERVICE_CONFIG_PATH - Path to service config YAML file (optional)"
            echo ""
            echo "Note: Environment variables override values from the service config file."
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

        # Check if external database is enabled
        yaml_external=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_PATH" | sed -n 's/^[[:space:]]*external:[[:space:]]*[\"'\'']*\([^\"'\''[:space:]]*\)[\"'\'']*.*$/\1/p' | head -1)

        if [ "$yaml_external" = "enabled" ]; then
            echo "External database mode detected"
        else
            echo "Internal database mode"
        fi

        # Read database configuration from YAML for both internal and external databases
        # Environment variables will take precedence if they are already set
        yaml_host=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_PATH" | sed -n 's/^[[:space:]]*hostname:[[:space:]]*[\"'\'']*\([^\"'\''[:space:]]*\)[\"'\'']*.*$/\1/p' | head -1)
        yaml_port=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_PATH" | sed -n 's/^[[:space:]]*port:[[:space:]]*\([^[:space:]]*\).*$/\1/p' | head -1)
        yaml_name=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_PATH" | sed -n 's/^[[:space:]]*name:[[:space:]]*[\"'\'']*\([^\"'\''[:space:]]*\)[\"'\'']*.*$/\1/p' | head -1)
        yaml_user=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_PATH" | sed -n 's/^[[:space:]]*user:[[:space:]]*[\"'\'']*\([^\"'\''[:space:]]*\)[\"'\'']*.*$/\1/p' | head -1)
        yaml_password=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_PATH" | sed -n 's/^[[:space:]]*userPassword:[[:space:]]*[\"'\'']*\([^\"'\''[:space:]]*\)[\"'\'']*.*$/\1/p' | head -1)

        # For external database, prefer YAML config over defaults
        # For internal database, use defaults if YAML values are not set
        if [ "$yaml_external" = "enabled" ]; then
            # External database: use YAML values, fall back to defaults only if YAML is empty
            [ -n "$yaml_host" ] && DB_HOST="$yaml_host"
            [ -n "$yaml_port" ] && DB_PORT="$yaml_port"
            [ -n "$yaml_name" ] && DB_NAME="$yaml_name"
            [ -n "$yaml_user" ] && DB_USER="$yaml_user"
            [ -n "$yaml_password" ] && DB_PASSWORD="$yaml_password"
        else
            # Internal database: use config file values only if environment variables are not already set
            [ -z "$DB_HOST" ] && [ -n "$yaml_host" ] && DB_HOST="$yaml_host"
            [ -z "$DB_PORT" ] && [ -n "$yaml_port" ] && DB_PORT="$yaml_port"
            [ -z "$DB_NAME" ] && [ -n "$yaml_name" ] && DB_NAME="$yaml_name"
            [ -z "$DB_USER" ] && [ -n "$yaml_user" ] && DB_USER="$yaml_user"
            [ -z "$DB_PASSWORD" ] && [ -n "$yaml_password" ] && DB_PASSWORD="$yaml_password"
        fi

        # Read SSL configuration from YAML
        yaml_ssl_mode=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_PATH" | sed -n 's/^[[:space:]]*sslmode:[[:space:]]*[\"'\'']*\([^\"'\''[:space:]]*\)[\"'\'']*.*$/\1/p' | head -1)
        yaml_ssl_cert=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_PATH" | sed -n 's/^[[:space:]]*sslcert:[[:space:]]*[\"'\'']*\([^\"'\''[:space:]]*\)[\"'\'']*.*$/\1/p' | head -1)
        yaml_ssl_key=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_PATH" | sed -n 's/^[[:space:]]*sslkey:[[:space:]]*[\"'\'']*\([^\"'\''[:space:]]*\)[\"'\'']*.*$/\1/p' | head -1)
        yaml_ssl_root_cert=$(sed -n '/^db:/,/^[^[:space:]]/p' "$SERVICE_CONFIG_PATH" | sed -n 's/^[[:space:]]*sslrootcert:[[:space:]]*[\"'\'']*\([^\"'\''[:space:]]*\)[\"'\'']*.*$/\1/p' | head -1)

        # Use SSL config from file only if environment variables are not already set
        [ -z "$DB_SSL_MODE" ] && [ -n "$yaml_ssl_mode" ] && DB_SSL_MODE="$yaml_ssl_mode"
        [ -z "$DB_SSL_CERT" ] && [ -n "$yaml_ssl_cert" ] && DB_SSL_CERT="$yaml_ssl_cert"
        [ -z "$DB_SSL_KEY" ] && [ -n "$yaml_ssl_key" ] && DB_SSL_KEY="$yaml_ssl_key"
        [ -z "$DB_SSL_ROOT_CERT" ] && [ -n "$yaml_ssl_root_cert" ] && DB_SSL_ROOT_CERT="$yaml_ssl_root_cert"
    fi
fi


# Validate required DB_* environment variables
: "${DB_USER:?DB_USER environment variable must be set}"
: "${DB_PASSWORD:?DB_PASSWORD environment variable must be set}"

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
