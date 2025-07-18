-- FlightCtl Database User Setup Script
-- This script creates the database users with appropriate permissions for production deployments
-- Environment variables that must be set before running:
--   ${DB_NAME} - Database name (default: flightctl)
--   ${DB_MIGRATION_USER} - Migration user name (default: flightctl_migrator)
--   ${DB_MIGRATION_PASSWORD} - Migration user password
--   ${DB_APP_USER} - Application user name (default: flightctl_app)
--   ${DB_APP_PASSWORD} - Application user password

-- Create the migration user with full privileges for schema changes
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '${DB_MIGRATION_USER}') THEN
        CREATE USER "${DB_MIGRATION_USER}" WITH PASSWORD '${DB_MIGRATION_PASSWORD}';
    END IF;
END $$;

-- Create the application user with limited privileges
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '${DB_APP_USER}') THEN
        CREATE USER "${DB_APP_USER}" WITH PASSWORD '${DB_APP_PASSWORD}';
    END IF;
END $$;

-- Grant database connection privileges
GRANT CONNECT ON DATABASE "${DB_NAME}" TO "${DB_MIGRATION_USER}";
GRANT CONNECT ON DATABASE "${DB_NAME}" TO "${DB_APP_USER}";

-- Grant schema usage privileges
GRANT USAGE ON SCHEMA public TO "${DB_MIGRATION_USER}";
GRANT USAGE ON SCHEMA public TO "${DB_APP_USER}";

-- Grant migration user minimum required privileges for schema migrations
-- Following the principle of least privilege, these are the specific privileges needed:
-- 1. CREATE on schema - for creating tables, indexes, sequences, functions, and triggers
-- 2. CREATE on database - for creating additional schemas if needed
-- Note: Migration user does NOT get data-plane permissions (SELECT, INSERT, UPDATE, DELETE)
-- for security reasons - migrations should only modify schema structure, not data
GRANT CREATE ON SCHEMA public TO "${DB_MIGRATION_USER}";
GRANT CREATE ON DATABASE "${DB_NAME}" TO "${DB_MIGRATION_USER}";

-- Grant additional privileges needed for schema operations only
-- Migration user should NOT have data-plane permissions (SELECT, INSERT, UPDATE, DELETE)
-- for security reasons - migrations only modify schema, not data
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO "${DB_MIGRATION_USER}";
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO "${DB_MIGRATION_USER}";

-- Grant migration user additional privileges for schema modifications
-- The CREATE privilege on schema allows creating, altering, and dropping tables, indexes, etc.
-- No additional ALTER/DROP privileges needed - these are handled by CREATE on schema

-- Grant application user limited privileges for data operations
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO "${DB_APP_USER}";
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO "${DB_APP_USER}";

-- Ensure future tables inherit the same permissions
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "${DB_APP_USER}";
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO "${DB_APP_USER}";

-- Create a function to grant permissions on newly created tables
-- This function uses pg_event_trigger_ddl_commands() to get the created objects
CREATE OR REPLACE FUNCTION grant_app_permissions()
RETURNS event_trigger AS $$
DECLARE
    obj record;
BEGIN
    FOR obj IN SELECT * FROM pg_event_trigger_ddl_commands() WHERE command_tag IN ('CREATE TABLE', 'CREATE SEQUENCE')
    LOOP
        IF obj.command_tag = 'CREATE TABLE' THEN
            EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO "${DB_APP_USER}"', obj.object_identity);
        END IF;
        IF obj.command_tag = 'CREATE SEQUENCE' THEN
            EXECUTE format('GRANT USAGE, SELECT ON %I TO "${DB_APP_USER}"', obj.object_identity);
        END IF;
    END LOOP;
EXCEPTION
    WHEN OTHERS THEN
        -- Log the error but don't fail the DDL operation
        RAISE WARNING 'Failed to grant permissions on %, error: %', obj.object_identity, SQLERRM;
END;
$$ LANGUAGE plpgsql;

-- Create event trigger to automatically grant permissions on new tables
DROP EVENT TRIGGER IF EXISTS grant_app_permissions_trigger;
CREATE EVENT TRIGGER grant_app_permissions_trigger
    ON ddl_command_end
    WHEN TAG IN ('CREATE TABLE', 'CREATE SEQUENCE')
    EXECUTE FUNCTION grant_app_permissions();

-- Create a function to grant permissions on all existing tables (fallback)
CREATE OR REPLACE FUNCTION grant_app_permissions_on_existing_tables()
RETURNS void AS $$
BEGIN
    -- Grant permissions on all existing tables
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO "${DB_APP_USER}"';
    EXECUTE 'GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO "${DB_APP_USER}"';
END;
$$ LANGUAGE plpgsql;

-- Grant permissions on existing tables now (in case migration already ran)
SELECT grant_app_permissions_on_existing_tables();

-- Display created users
SELECT rolname, rolsuper, rolcreaterole, rolcreatedb, rolcanlogin
FROM pg_roles
WHERE rolname IN ('${DB_MIGRATION_USER}', '${DB_APP_USER}')
ORDER BY rolname;
