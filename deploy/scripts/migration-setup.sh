#!/bin/bash

set -e

echo "Setting up database migration user..."

# Create migration user if not exists
echo "Creating migration user: $DB_MIGRATION_USER"
PGPASSWORD="$DB_ADMIN_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "
DO \$\$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '$DB_MIGRATION_USER') THEN
        CREATE USER \"$DB_MIGRATION_USER\" WITH PASSWORD '$DB_MIGRATION_PASSWORD';
    END IF;
END \$\$;"

# Grant migration user privileges
echo "Granting privileges to migration user..."
PGPASSWORD="$DB_ADMIN_PASSWORD" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_ADMIN_USER" -d "$DB_NAME" -c "
GRANT CONNECT ON DATABASE \"$DB_NAME\" TO \"$DB_MIGRATION_USER\";
GRANT USAGE, CREATE ON SCHEMA public TO \"$DB_MIGRATION_USER\";
GRANT CREATE ON DATABASE \"$DB_NAME\" TO \"$DB_MIGRATION_USER\";"

echo "Migration user setup completed. Running database migration..."
/usr/local/bin/flightctl-db-migrate