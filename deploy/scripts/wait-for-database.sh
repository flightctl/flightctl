#!/bin/bash
set -euo pipefail

# Waits until a PostgreSQL database is ready for connections.
# Uses DB_* env vars and optional flags: --timeout, --sleep, --connection-timeout.

# Initialize defaults
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-180}"
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
            echo "  DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASSWORD - Database connection details"
            echo "  DB_USER_TYPE - Optional user type for logging"
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

# Validate required environment variables
: "${DB_HOST:?DB_HOST environment variable must be set}"
: "${DB_PORT:?DB_PORT environment variable must be set}"
: "${DB_NAME:?DB_NAME environment variable must be set}"
: "${DB_USER:?DB_USER environment variable must be set}"

# Set PGPASSWORD from DB_PASSWORD if not already set
export PGPASSWORD="${PGPASSWORD:-${DB_PASSWORD:?DB_PASSWORD or PGPASSWORD environment variable must be set}}"

# Determine user type from DB_USER_TYPE environment variable if set
USER_TYPE="${DB_USER_TYPE:-unknown}"

# Log connection details
echo "Waiting for PostgreSQL database to be ready..."
echo "Connection details:"
echo "  Host: ${DB_HOST}"
echo "  Port: ${DB_PORT}"
echo "  Database: ${DB_NAME}"
echo "  User: ${DB_USER} (${USER_TYPE})"
echo "  Timeout: ${TIMEOUT_SECONDS} seconds"
echo "  Sleep interval: ${SLEEP_INTERVAL} seconds"
echo "  Connection timeout: ${CONNECTION_TIMEOUT} seconds"
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
          psql -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" -d "${DB_NAME}" -tAq -c "SELECT 1" >/dev/null; } 2>&1
    )
    connection_result=$?
    set -e
    
    # Handle timeout case (exit code 124)
    if [ $connection_result -eq 124 ]; then
        error_output="Connection timeout after ${CONNECTION_TIMEOUT} seconds"
        connection_result=1
    fi
    
    if [ $connection_result -eq 0 ]; then
        echo "✓ Database is ready and accepting connections!"
        echo "Total wait time: ${elapsed_time} seconds"
        exit 0
    else
        echo "Connection failed: $error_output"
        echo "Database not ready, waiting ${SLEEP_INTERVAL} seconds..."
    fi
    
    sleep "${SLEEP_INTERVAL}"
done

# Timeout reached
final_time=$(date +%s)
total_elapsed=$((final_time - start_time))
echo ""
echo "✗ ERROR: Database failed to become ready within ${TIMEOUT_SECONDS} seconds" >&2
echo "Total elapsed time: ${total_elapsed} seconds" >&2
echo "Connection string: postgresql://${DB_USER}@${DB_HOST}:${DB_PORT}/${DB_NAME}" >&2
echo "Last error: $error_output" >&2
exit 1
