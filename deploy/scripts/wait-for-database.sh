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

# Function to read value from YAML file using yaml_helpers.py
read_yaml_value() {
    local key="$1"
    local config_file="$2"

    if command -v python3 &> /dev/null && [ -f "$config_file" ]; then
        python3 ./deploy/scripts/yaml_helpers.py extract "$key" "$config_file" --default ""
    else
        echo ""
    fi
}

# Load database configuration from SERVICE_CONFIG_PATH if provided
if [ -n "${SERVICE_CONFIG_PATH:-}" ]; then
    if [ ! -f "$SERVICE_CONFIG_PATH" ]; then
        echo "Warning: SERVICE_CONFIG_PATH is set but file not found: $SERVICE_CONFIG_PATH" >&2
    elif ! command -v jq &> /dev/null || ! command -v python3 &> /dev/null; then
        echo "Warning: jq or python3 command not found, cannot read service config file" >&2
    else
        echo "Loading database configuration from: $SERVICE_CONFIG_PATH"

        # Read values from YAML into DB_* variables if not already set
        : "${DB_HOST:=$(read_yaml_value ".db.hostname" "$SERVICE_CONFIG_PATH")}"
        : "${DB_PORT:=$(read_yaml_value ".db.port" "$SERVICE_CONFIG_PATH")}"
        : "${DB_NAME:=$(read_yaml_value ".db.name" "$SERVICE_CONFIG_PATH")}"
        : "${DB_USER:=$(read_yaml_value ".db.user" "$SERVICE_CONFIG_PATH")}"
        : "${DB_SSL_MODE:=$(read_yaml_value ".db.sslmode" "$SERVICE_CONFIG_PATH")}"
        : "${DB_SSL_CERT:=$(read_yaml_value ".db.sslcert" "$SERVICE_CONFIG_PATH")}"
        : "${DB_SSL_KEY:=$(read_yaml_value ".db.sslkey" "$SERVICE_CONFIG_PATH")}"
        : "${DB_SSL_ROOT_CERT:=$(read_yaml_value ".db.sslrootcert" "$SERVICE_CONFIG_PATH")}"
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
