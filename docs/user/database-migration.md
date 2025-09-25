# Flight Control Database Access Restriction

This document describes the database access restriction system implemented in Flight Control to enhance security by separating database privileges between migration operations and runtime services.

## Overview

Flight Control uses a three-user database access model for production deployments:

1. **`admin`** - Full superuser privileges for database administration and user creation
2. **`flightctl_migrator`** - Limited privileges for schema creation and migrations (following principle of least privilege)
3. **`flightctl_app`** - Limited privileges for runtime data operations (SELECT, INSERT, UPDATE, DELETE)

This separation follows the principle of least privilege, ensuring that production services cannot accidentally modify database schema or structure, while maintaining clear separation between administrative, migration, and runtime operations.

**Security Note**: The migration user has been designed with minimal privileges required for database schema operations. It does **not** have SUPERUSER, CREATEROLE, or CREATEDB privileges, which would grant excessive permissions like bypassing row-level security, creating/dropping roles, creating databases, or altering any database object. It also does **not** have data-plane permissions (SELECT, INSERT, UPDATE, DELETE) on tables, ensuring it cannot access or modify production data. Instead, it has only the specific privileges needed for schema migrations: CREATE privileges on schemas and databases, sequence usage for auto-increment fields, and function execution for migration operations.

## Database Users

Flight Control uses exactly **3 database users** for secure operation:

### 1. Admin User (`admin`)

- **Purpose**: Database administration, user creation, and initial setup
- **Privileges**:
  - SUPERUSER (full administrative privileges)
  - Used to create other database users
  - Database initialization and administrative tasks
- **When used**: Database container initialization and migration job user setup
- **Password**: `masterPassword` secret key

### 2. Migration User (`flightctl_migrator`)

- **Purpose**: Schema creation, table modifications, and database migrations
- **Privileges** (following principle of least privilege):
  - CONNECT to database
  - CREATE on schema and database (for creating tables, indexes, sequences, functions, triggers)
  - USAGE, SELECT on sequences in the public schema (needed for sequence operations during schema creation)
  - EXECUTE on all functions in the public schema (needed for migration functions)
  - **Note**: Migration user does NOT have data-plane permissions (SELECT, INSERT, UPDATE, DELETE) for security reasons - migrations only modify schema structure, not data
- **When used**: Only during database migrations and initial setup
- **Password**: `migrationPassword` secret key

### 3. Application User (`flightctl_app`)

- **Purpose**: Runtime data operations for all Flight Control services
- **Privileges**:
  - CONNECT to database
  - SELECT, INSERT, UPDATE, DELETE on all tables
  - USAGE, SELECT on all sequences
  - Automatic permission inheritance for new tables (via event triggers)
- **When used**: All runtime service operations (API, Worker, Periodic, Alert-Exporter)
- **Password**: `userPassword` secret key

## Environment Variables

### For Runtime Services (API, Worker, Periodic, Alert-Exporter)

```bash
# Application database user (used by all services during runtime)
DB_USER=flightctl_app
DB_PASSWORD=<userPassword from secret>
```

### For Migration Operations Only

```bash
# Migration database user (used only for migrations)
DB_MIGRATION_USER=flightctl_migrator
DB_MIGRATION_PASSWORD=<migrationPassword from secret>
```

### For Database Administration (Migration Job Setup Phase)

```bash
# Admin database user (used only for user creation and setup)
DB_ADMIN_USER=admin
DB_ADMIN_PASSWORD=<masterPassword from secret>
```

> **Security Note**:
>
> - Runtime services should **never** have access to migration or admin user credentials
> - Only dedicated migration jobs/commands should have migration credentials
> - Only database setup operations should have admin credentials
> - This maintains proper privilege separation across all operations

## Deployment Methods

### Helm Deployment

The Helm deployment automatically handles user creation and migration:

1. **Database Secrets**: Contains all 3 user credentials:
   - `masterUser` / `masterPassword`: Admin user for database setup
   - `user` / `userPassword`: Application user for runtime services
   - `migrationUser` / `migrationPassword`: Migration user for schema changes
2. **Migration Job**: Post-install/post-upgrade hook that:
   - Uses admin user to create migration and application users with appropriate privileges
   - Runs database migrations using the migration user
   - Sets up automatic permission granting for future tables
3. **Service Configuration**: All runtime services use the application user only

#### Migration Job Process

```bash
# 1. Wait for database to be ready
# 2. Execute consolidated setup script from container image (/app/deploy/scripts/setup_database_users.sh) which:
#    - Verifies database connectivity
#    - Creates flightctl_migrator and flightctl_app users
#    - Grants appropriate privileges
#    - Sets up automatic permission inheritance
# 3. Run database migrations using migration user
```

### Quadlet Deployment

The quadlet deployment includes integrated user setup and migration:

1. **Database Container**: Uses `flightctl_app` as the default user, with `admin` as the master user
2. **User Creation**: Automatic setup of all 3 users during deployment
3. **Migration Execution**: Runs migrations using a temporary container with migration user
4. **Service Configuration**: All services connect using the application user

#### Deployment Process

```bash
# 1. Start database container
# 2. Execute consolidated setup script (setup_database_users.sh)
# 3. Run migrations using flightctl-db-migrate command
# 4. Start all services with application user
```

## Migration Process

### Automatic Migration

Database migrations are handled by dedicated tools and jobs:

- **Helm**: Install runs a regular migration Job; upgrade runs a pre-upgrade dry-run first, then the migration Job if validation succeeds
- **Quadlet deployments**: Migration runs during the deployment script
- **Manual deployments**: Use the `flightctl-db-migrate` command

#### Helm Migration Configuration

Helm upgrades use pre-upgrade hooks for migration validation and execution:

```yaml
# In values.yaml or via --set
upgradeHooks:
  databaseMigrationDryRun: true  # Enable pre-upgrade migration dry-run (default: true)
```

**Migration Execution Order**:

1. Pre-upgrade hook: migration dry-run (validation only). Any failure aborts the upgrade.
2. Pre-upgrade hook: actual migration (expand-only/backward-compatible).
3. Helm applies the rest of the release changes.

### Manual Migration

You can run migrations manually using the dedicated migration command:

```bash
# Set migration environment variables
export DB_USER=flightctl_migrator
export DB_PASSWORD=<migration_password>
export DB_MIGRATION_USER=flightctl_migrator
export DB_MIGRATION_PASSWORD=<migration_password>

# Run migration
flightctl-db-migrate

# Run dry-run validation only (without applying changes)
flightctl-db-migrate --dry-run
```

> **Note**: The migration command automatically uses the migration user credentials and runs all necessary database schema updates.

## Security Features

### Privilege Separation

- **Runtime services** cannot create or modify database schema
- **Migration operations** are isolated to specific deployment phases
- **Credential separation**: Runtime services only have application user credentials, never migration credentials
- **Automatic permission granting** ensures new tables are accessible to application user

### Permission Inheritance

An event trigger automatically grants appropriate permissions to the application user when new tables or sequences are created:

```sql
CREATE EVENT TRIGGER grant_app_permissions_trigger
    ON ddl_command_end
    WHEN TAG IN ('CREATE TABLE', 'CREATE SEQUENCE')
    EXECUTE FUNCTION grant_app_permissions();
```

### Database User Validation

Services validate database connectivity and user privileges during startup, providing clear error messages if misconfigured.

## Troubleshooting

### Common Issues

#### 1. Permission Denied Errors

**Symptom**: Services fail with "permission denied" database errors
**Solution**:

- Verify the application user has been created and granted proper privileges
- Check that migrations have been run with the migration user
- Ensure the application is connecting with the correct user credentials

#### 2. Migration Failures

**Symptom**: Migration job fails during deployment
**Solution**:

- Check database connectivity and admin user privileges
- Verify migration user credentials
- Review migration logs for specific error details

#### 3. User Creation Errors

**Symptom**: Database user creation fails during setup
**Solution**:

- Ensure admin user has CREATEDB and superuser privileges
- Check database connection parameters
- Verify password complexity requirements

#### 4. Services Attempting to Run Migrations

**Symptom**: Services fail on startup with messages about migrations or schema creation
**Solution**:

- Verify that migrations have been completed during deployment phase
- Check that services are using application user credentials (not migration credentials)
- Ensure the database schema is properly set up before starting services

### Manual User Setup

If automatic user setup fails, you can manually create users using the consolidated database setup script:

```bash
# Run the setup script (recommended approach used by all deployment methods)
./deploy/scripts/setup_database_users.sh

# Or with custom environment variables
export DB_HOST=your_db_host
export DB_PORT=5432
export DB_NAME=flightctl
export DB_ADMIN_USER=admin
export DB_ADMIN_PASSWORD=your_admin_password
export DB_MIGRATION_USER=flightctl_migrator
export DB_MIGRATION_PASSWORD=your_migration_password
export DB_APP_USER=flightctl_app
export DB_APP_PASSWORD=your_app_password

./deploy/scripts/setup_database_users.sh
```

  > **Note**: All deployment methods (Helm, Quadlet, Manual) use the same consolidated setup script. For Helm deployments, the script is already included in the container image at `/app/deploy/scripts/setup_database_users.sh`. For other deployments, it's located at `deploy/scripts/setup_database_users.sh`. This script provides database connectivity checks, proper error handling, and uses the canonical SQL file, ensuring consistency and reliability across all deployment approaches.

### Verification Commands

Check database secret structure:

```bash
# Verify all 3 users are present in the secret
kubectl get secret flightctl-db-secret -o jsonpath='{.data}' | jq 'keys'
# Should show: ["masterPassword", "masterUser", "migrationPassword", "migrationUser", "user", "userPassword"]

# Check individual user credentials (base64 encoded)
kubectl get secret flightctl-db-secret -o jsonpath='{.data.masterUser}' | base64 -d
kubectl get secret flightctl-db-secret -o jsonpath='{.data.user}' | base64 -d
kubectl get secret flightctl-db-secret -o jsonpath='{.data.migrationUser}' | base64 -d
```

Check database user privileges:

```sql
-- List all 3 database users and their roles
SELECT rolname, rolsuper, rolcreaterole, rolcreatedb, rolcanlogin
FROM pg_roles
WHERE rolname IN ('admin', 'flightctl_migrator', 'flightctl_app')
ORDER BY rolname;

-- Check table permissions for application user
SELECT tablename, privileges
FROM (
    SELECT schemaname, tablename,
           array_to_string(array_agg(privilege_type), ', ') as privileges
    FROM information_schema.table_privileges
    WHERE grantee = 'flightctl_app' AND schemaname = 'public'
    GROUP BY schemaname, tablename
) t;
```

## Best Practices

1. **Use different passwords** for all 3 database users (admin, migration, application)
2. **Rotate passwords regularly** using your secret management system
3. **Monitor database permissions** to ensure proper access restrictions
4. **Test migrations** in non-production environments first
5. **Use dry-run validation** to verify migration scripts before applying them in production
6. **Review migration logs** for any permission or connectivity issues
7. **Audit user access** - ensure services only have the minimum required credentials:
   - Runtime services: Only application user credentials
   - Migration operations: Only migration user credentials
   - Database setup: Only admin user credentials
