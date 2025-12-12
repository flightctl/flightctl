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
        _psql -U postgres -c $'DO $$BEGIN
            IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = quote_ident(''${POSTGRESQL_MASTER_USER}'')) THEN
                EXECUTE format(
                  ''CREATE ROLE %I WITH LOGIN PASSWORD %L'',
                  ''${POSTGRESQL_MASTER_USER}'', ''${POSTGRESQL_MASTER_PASSWORD}'');
            END IF;
        END$$;'
    fi

    # Grant superuser privileges
    _psql -U postgres -c "ALTER ROLE \"${POSTGRESQL_MASTER_USER}\" WITH SUPERUSER CREATEDB CREATEROLE;"
    echo "Successfully granted superuser privileges to ${POSTGRESQL_MASTER_USER}"
else
    echo "POSTGRESQL_MASTER_USER not set, skipping superuser configuration"
fi

echo "Setting up Flight Control database users for production deployment..."

# Get passwords from environment variables (set by podman secrets)
: "${POSTGRESQL_PASSWORD:?POSTGRESQL_PASSWORD must be set}"
DB_APP_PASSWORD="${POSTGRESQL_PASSWORD}"

# Database user setup - using the admin user we just configured
DB_NAME="flightctl"
DB_ADMIN_USER="${POSTGRESQL_MASTER_USER:-postgres}"
: "${POSTGRESQL_MASTER_PASSWORD:?POSTGRESQL_MASTER_PASSWORD must be set}"
# propagate the password to psql
export PGPASSWORD="${POSTGRESQL_MASTER_PASSWORD}"
DB_APP_USER="flightctl_app"

# Note: Migration user creation is now handled by the migration service using
# the setup_database_users.sh script to avoid credential duplication

# Create the application user with limited privileges
echo "Creating application user: $DB_APP_USER"
_psql -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "
DO \$\$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$DB_APP_USER') THEN
        EXECUTE format('CREATE USER %I WITH PASSWORD %L', '$DB_APP_USER', '$DB_APP_PASSWORD');
    END IF;
END \$\$;"

# Grant database connection privileges
echo "Granting connection privileges..."
_psql -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "GRANT CONNECT ON DATABASE \"$DB_NAME\" TO \"$DB_APP_USER\";"

# Grant schema usage privileges
_psql -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "GRANT USAGE ON SCHEMA public TO \"$DB_APP_USER\";"

# Note: Migration user privileges are now handled by the migration service

# Grant data privileges for application user
echo "Granting data privileges to $DB_APP_USER..."
_psql -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO \"$DB_APP_USER\";"
_psql -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO \"$DB_APP_USER\";"

# Set up automatic privilege granting for new tables
echo "Setting up automatic privilege granting for new tables..."
_psql -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO \"$DB_APP_USER\";"
_psql -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO \"$DB_APP_USER\";"

# Create function to grant permissions on existing tables (for post-migration)
cat << EOF | _psql -U "$DB_ADMIN_USER" -d "$DB_NAME"
CREATE OR REPLACE FUNCTION grant_app_permissions_on_existing_tables()
RETURNS void AS \$function\$
BEGIN
    -- Grant table permissions
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I', '$DB_APP_USER');
    -- Grant sequence permissions
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %I', '$DB_APP_USER');
END;
\$function\$ LANGUAGE plpgsql;
EOF

# immediately grant permissions on the current schema objects
echo "Granting privileges on existing objects..."
_psql -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "SELECT grant_app_permissions_on_existing_tables();"

# Create event trigger for automatic permission granting
cat << EOF | _psql -U "$DB_ADMIN_USER" -d "$DB_NAME"
CREATE OR REPLACE FUNCTION grant_app_permissions()
RETURNS event_trigger AS \$function\$
BEGIN
    -- Grant permissions on newly created tables/sequences
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I', '$DB_APP_USER');
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %I', '$DB_APP_USER');
END;
\$function\$ LANGUAGE plpgsql;
EOF

# Drop existing trigger if it exists, then create new one
_psql -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "DROP EVENT TRIGGER IF EXISTS grant_app_permissions_trigger;"
_psql -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "
CREATE EVENT TRIGGER grant_app_permissions_trigger
    ON ddl_command_end
    WHEN TAG IN ('CREATE TABLE', 'CREATE SEQUENCE')
    EXECUTE FUNCTION grant_app_permissions();"

echo "Database user setup completed successfully!"
echo "Created users:"
echo "  - Application user: $DB_APP_USER (data access privileges)"
echo "  - Automatic permission granting enabled for new tables"
echo "Note: Migration user creation is handled by the migration service"

echo "Database configuration completed successfully"
