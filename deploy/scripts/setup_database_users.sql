-- Flight Control Database User Setup Script
-- This script creates the database users with appropriate permissions for production deployments
-- Environment variables that must be set before running:
--   ${DB_NAME} - Database name (default: flightctl)
--   ${DB_MIGRATION_USER} - Migration user name (default: flightctl_migrator)
--   ${DB_MIGRATION_PASSWORD} - Migration user password
--   ${DB_APP_USER} - Application user name (default: flightctl_app)
--   ${DB_APP_PASSWORD} - Application user password

-- Create users using simple SQL (will ignore errors if they already exist)
\set ON_ERROR_STOP off

-- Create the migration user with full privileges for schema changes
CREATE USER "${DB_MIGRATION_USER}" WITH PASSWORD $$${DB_MIGRATION_PASSWORD}$$;

-- Create the application user with limited privileges
CREATE USER "${DB_APP_USER}" WITH PASSWORD $$${DB_APP_PASSWORD}$$;

-- Ensure existing users get their passwords updated for true idempotency
ALTER USER "${DB_MIGRATION_USER}" WITH PASSWORD $$${DB_MIGRATION_PASSWORD}$$;
ALTER USER "${DB_APP_USER}" WITH PASSWORD $$${DB_APP_PASSWORD}$$;

\set ON_ERROR_STOP on

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
ALTER DEFAULT PRIVILEGES FOR ROLE "${DB_APP_USER}" IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "${DB_APP_USER}";
ALTER DEFAULT PRIVILEGES FOR ROLE "${DB_APP_USER}" IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO "${DB_APP_USER}";


-- Create function to grant permissions on existing tables (for post-migration)
CREATE OR REPLACE FUNCTION grant_app_permissions_on_existing_tables()
RETURNS void AS '
BEGIN
    -- Grant table permissions
    EXECUTE format(''GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I'', ''${DB_APP_USER}'');
    -- Grant sequence permissions
    EXECUTE format(''GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %I'', ''${DB_APP_USER}'');
END;
' LANGUAGE plpgsql;

-- Create event trigger function for automatic permission granting
CREATE OR REPLACE FUNCTION grant_app_permissions()
RETURNS event_trigger AS '
BEGIN
    -- Grant permissions on newly created tables/sequences
    EXECUTE format(''GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I'', ''${DB_APP_USER}'');
    EXECUTE format(''GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %I'', ''${DB_APP_USER}'');
END;
' LANGUAGE plpgsql;

-- Drop existing trigger if it exists, then create new one
DROP EVENT TRIGGER IF EXISTS grant_app_permissions_trigger;
CREATE EVENT TRIGGER grant_app_permissions_trigger
    ON ddl_command_end
    WHEN TAG IN ('CREATE TABLE', 'CREATE SEQUENCE')
    EXECUTE FUNCTION grant_app_permissions();

-- Display created users
SELECT rolname, rolsuper, rolcreaterole, rolcreatedb, rolcanlogin
FROM pg_roles
WHERE rolname IN ('${DB_MIGRATION_USER}', '${DB_APP_USER}')
ORDER BY rolname;
