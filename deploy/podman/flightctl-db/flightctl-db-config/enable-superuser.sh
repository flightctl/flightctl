#!/usr/bin/env bash

set -e

_psql () { psql --set ON_ERROR_STOP=1 "$@" ; }

# Wait for PostgreSQL to be ready
echo "Waiting for PostgreSQL to be fully initialized..."
# wait up to 120 s
for _ in {1..120}; do
    pg_isready -U postgres && break
    echo "PostgreSQL not ready, waiting…"
    sleep 1
done
pg_isready -U postgres || { echo "PostgreSQL did not become ready"; exit 1; }

echo "PostgreSQL is ready, configuring permissions..."

# Ensure POSTGRESQL_MASTER_USER is treated as a superuser
if [ -n "${POSTGRESQL_MASTER_USER}" ]; then
    echo "Granting superuser privileges to ${POSTGRESQL_MASTER_USER}"
    
    # Check if user exists, create if it doesn't
    if ! _psql -U postgres -tAc "SELECT 1 FROM pg_roles WHERE rolname = '${POSTGRESQL_MASTER_USER}'" | grep -q 1; then
        echo "Creating master user ${POSTGRESQL_MASTER_USER}"
        _psql -U postgres -c "CREATE ROLE \"${POSTGRESQL_MASTER_USER}\" WITH LOGIN PASSWORD '${POSTGRESQL_MASTER_PASSWORD}';"
    fi
    
    # Grant superuser privileges
    _psql -U postgres -c "ALTER ROLE \"${POSTGRESQL_MASTER_USER}\" WITH SUPERUSER CREATEDB CREATEROLE;"
    echo "Successfully granted superuser privileges to ${POSTGRESQL_MASTER_USER}"
else
    echo "POSTGRESQL_MASTER_USER not set, skipping superuser configuration"
fi

echo "Database configuration completed successfully"
