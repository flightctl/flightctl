-- Flight Control Database User Setup Script
-- This script creates the database users with appropriate permissions for production deployments
--
-- Required psql variables:
--   db_name            - Database name (e.g. flightctl)                  [pass via -v]
--   migration_user     - Migration user name (e.g. flightctl_migrator)   [pass via -v]
--   migration_password - Migration user password                         [set via \set in stdin]
--   app_user           - Application user name (e.g. flightctl_app)      [pass via -v]
--   app_password       - Application user password                       [set via \set in stdin]
--
-- Passwords are piped through stdin as \set commands by the calling script
-- (setup_database_users.sh) so they never appear in process arguments.

-- Create users using simple SQL (will ignore errors if they already exist)
\set ON_ERROR_STOP off

-- Create the migration user with full privileges for schema changes
CREATE USER :"migration_user" WITH PASSWORD :'migration_password';

-- Create the application user with limited privileges
CREATE USER :"app_user" WITH PASSWORD :'app_password';

-- Ensure existing users get their passwords updated for true idempotency
ALTER USER :"migration_user" WITH PASSWORD :'migration_password';
ALTER USER :"app_user" WITH PASSWORD :'app_password';

\set ON_ERROR_STOP on

-- Grant database connection privileges
GRANT CONNECT ON DATABASE :"db_name" TO :"migration_user";
GRANT CONNECT ON DATABASE :"db_name" TO :"app_user";

-- Grant schema usage privileges
GRANT USAGE ON SCHEMA public TO :"migration_user";
GRANT USAGE ON SCHEMA public TO :"app_user";

-- Grant migration user privileges for schema migrations
-- Following the principle of least privilege, these are the specific privileges needed:
-- 1. CREATE on schema - for creating tables, indexes, sequences, functions, and triggers
-- 2. CREATE on database - for creating additional schemas if needed
GRANT CREATE ON SCHEMA public TO :"migration_user";
GRANT CREATE ON DATABASE :"db_name" TO :"migration_user";

-- Grant additional privileges needed for schema and post-migration operations
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO :"migration_user";
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO :"migration_user";

-- Grant migration user data-plane permissions WITH GRANT OPTION
-- This allows the migrator to grant these permissions to the application user
-- after running migrations, enabling support for external databases without admin access
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public
    TO :"migration_user" WITH GRANT OPTION;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public
    TO :"migration_user" WITH GRANT OPTION;

-- Ensure future tables also get WITH GRANT OPTION for migrator
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO :"migration_user" WITH GRANT OPTION;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO :"migration_user" WITH GRANT OPTION;

-- Grant application user limited privileges for data operations
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO :"app_user";
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO :"app_user";

-- Ensure future tables inherit the same permissions
ALTER DEFAULT PRIVILEGES FOR ROLE :"app_user" IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO :"app_user";
ALTER DEFAULT PRIVILEGES FOR ROLE :"app_user" IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO :"app_user";

-- Persist app user name as a database-level GUC so that the event trigger
-- function can resolve it in future sessions without envsubst.
ALTER DATABASE :"db_name" SET flightctl.app_user = :'app_user';
SET flightctl.app_user = :'app_user';

-- Create function to grant permissions on existing tables (for post-migration)
CREATE OR REPLACE FUNCTION grant_app_permissions_on_existing_tables()
RETURNS void AS $$
DECLARE
    v_app_user text := current_setting('flightctl.app_user');
BEGIN
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I', v_app_user);
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %I', v_app_user);
END;
$$ LANGUAGE plpgsql;

-- Create event trigger function for automatic permission granting
CREATE OR REPLACE FUNCTION grant_app_permissions()
RETURNS event_trigger AS $$
DECLARE
    v_app_user text := current_setting('flightctl.app_user');
BEGIN
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I', v_app_user);
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %I', v_app_user);
END;
$$ LANGUAGE plpgsql;

-- Drop existing trigger if it exists, then create new one
DROP EVENT TRIGGER IF EXISTS grant_app_permissions_trigger;
CREATE EVENT TRIGGER grant_app_permissions_trigger
    ON ddl_command_end
    WHEN TAG IN ('CREATE TABLE', 'CREATE SEQUENCE')
    EXECUTE FUNCTION grant_app_permissions();

-- Display created users
SELECT rolname, rolsuper, rolcreaterole, rolcreatedb, rolcanlogin
FROM pg_roles
WHERE rolname IN (:'migration_user', :'app_user')
ORDER BY rolname;
