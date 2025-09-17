#!/bin/bash
set -euo pipefail

# Waits until a PostgreSQL database is ready for connections.
# Uses PG* env vars and optional flags: --timeout, --sleep, --connection-timeout.
# Optionally reads database configuration from a YAML file specified by SERVICE_CONFIG_PATH.
# Environment variables override values from the YAML file.

# Initialize defaults
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-60}"
SLEEP_INTERVAL="${SLEEP_INTERVAL:-2}"
CONNECTION_TIMEOUT="${CONNECTION_TIMEOUT:-3}"

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --timeout=*)
            TIMEOUT_SECONDS="${1#*=}"
            shift
            ;;
        --sleep=*)
            SLEEP_INTERVAL="${1#*=}"
            shift
            ;;
        --connection-timeout=*)
            CONNECTION_TIMEOUT="${1#*=}"
            shift
            ;;
        --timeout)
            TIMEOUT_SECONDS="$2"
            shift 2
            ;;
        --sleep)
            SLEEP_INTERVAL="$2"
            shift 2
            ;;
        --connection-timeout)
            CONNECTION_TIMEOUT="$2"
            shift 2
            ;;
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
            echo "  PGHOST, PGUSER, PGDATABASE, PGPASSWORD - PostgreSQL connection details (required)"
            echo "  PGPORT - PostgreSQL port (optional, default: 5432)"
            echo "  PGSSLMODE, PGSSLCERT, PGSSLKEY, PGSSLROOTCERT - PostgreSQL SSL configuration (optional)"
            echo "  SERVICE_CONFIG_PATH - Path to service config YAML file (optional)"
            echo ""
            echo "Note: Environment variables override values from the service config file."
            exit 0
            ;;
        --*)
            echo "Unknown option $1" >&2
            echo "Use --help for usage information" >&2
            exit 1
            ;;
        *)
            echo "Unknown argument: $1" >&2
            echo "Use --help for usage information" >&2
            exit 1
            ;;
    esac
done

# Function to read value from YAML file using yq
read_yaml_value() {
    local key="$1"
    local config_file="$2"

    if command -v yq &> /dev/null && [ -f "$config_file" ]; then
        yq eval "$key // \"\"" "$config_file" 2>/dev/null || echo ""
    else
        echo ""
    fi
}

# Load database configuration from SERVICE_CONFIG_PATH if provided
if [ -n "${SERVICE_CONFIG_PATH:-}" ]; then
    if [ ! -f "$SERVICE_CONFIG_PATH" ]; then
        echo "Warning: SERVICE_CONFIG_PATH is set but file not found: $SERVICE_CONFIG_PATH" >&2
    elif ! command -v yq &> /dev/null; then
        echo "Warning: yq command not found, cannot read service config file" >&2
    else
        echo "Loading database configuration from: $SERVICE_CONFIG_PATH"

        # Read values from YAML, but only if corresponding env vars are not already set
        : "${PGHOST:=$(read_yaml_value ".db.hostname" "$SERVICE_CONFIG_PATH")}"
        : "${PGPORT:=$(read_yaml_value ".db.port" "$SERVICE_CONFIG_PATH")}"
        : "${PGDATABASE:=$(read_yaml_value ".db.name" "$SERVICE_CONFIG_PATH")}"
        : "${PGUSER:=$(read_yaml_value ".db.user" "$SERVICE_CONFIG_PATH")}"
        : "${PGSSLMODE:=$(read_yaml_value ".db.sslmode" "$SERVICE_CONFIG_PATH")}"
        : "${PGSSLCERT:=$(read_yaml_value ".db.sslcert" "$SERVICE_CONFIG_PATH")}"
        : "${PGSSLKEY:=$(read_yaml_value ".db.sslkey" "$SERVICE_CONFIG_PATH")}"
        : "${PGSSLROOTCERT:=$(read_yaml_value ".db.sslrootcert" "$SERVICE_CONFIG_PATH")}"
    fi
fi

# Validate arguments are numeric
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

# Set defaults if not provided from env or config file
: "${PGPORT:=5432}"
: "${PGHOST:=flightctl-db}"
: "${PGDATABASE:=flightctl}"

# Validate required environment variables
: "${PGUSER:?PGUSER environment variable must be set}"
: "${PGPASSWORD:?PGPASSWORD environment variable must be set}"

# Log connection details
echo "Waiting for PostgreSQL database to be ready..."
echo "Connection details:"
echo "  Host: ${PGHOST}"
echo "  Port: ${PGPORT}"
echo "  Database: ${PGDATABASE}"
echo "  User: ${PGUSER}"
echo "  Timeout: ${TIMEOUT_SECONDS} seconds"
echo "  Sleep interval: ${SLEEP_INTERVAL} seconds"
echo "  Connection timeout: ${CONNECTION_TIMEOUT} seconds"

# Log SSL configuration (non-sensitive info only)
if [ -n "${PGSSLMODE:-}" ]; then
    echo "SSL configuration:"
    echo "  SSL Mode: ${PGSSLMODE}"
    [ -n "${PGSSLCERT:-}" ] && echo "  SSL Certificate: configured"
    [ -n "${PGSSLKEY:-}" ] && echo "  SSL Key: configured"
    [ -n "${PGSSLROOTCERT:-}" ] && echo "  SSL Root Certificate: configured"
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
          env PGHOST="$PGHOST" PGPORT="$PGPORT" PGUSER="$PGUSER" \
              PGDATABASE="$PGDATABASE" PGPASSWORD="$PGPASSWORD" \
              ${PGSSLMODE:+PGSSLMODE="$PGSSLMODE"} \
              ${PGSSLCERT:+PGSSLCERT="$PGSSLCERT"} \
              ${PGSSLKEY:+PGSSLKEY="$PGSSLKEY"} \
              ${PGSSLROOTCERT:+PGSSLROOTCERT="$PGSSLROOTCERT"} \
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
