# Configuring External PostgreSQL Database

Flight Control supports using an external PostgreSQL database instead of the built-in database deployment. using an existing database, implementing specific security policies or high availability, or using a managed database service.

## Prerequisites

### Database Requirements

- **PostgreSQL 16 or later**
- Database instance accessible from your Flight Control deployment
- Admin access to create users and databases

### Required Extensions

- **pg_trgm extension** must be enabled for text search and pattern matching

### Data Types Support

- **JSONB data type** (for labels, annotations, and complex data structures)
- **UUID data type** (for primary keys and foreign keys)
- **Composite primary keys** (OrgID + Name combinations)
- **Foreign key constraints** with CASCADE DELETE

### Index Types

- **GIN indexes** on JSONB columns for efficient querying
- **B-Tree indexes** for standard column indexing
- **GIN indexes with gin_trgm_ops** for text search operations

### Database Features

- **Triggers support** (for maintaining device labels synchronization)
- **PL/pgSQL functions support** (for trigger functions)
- **Connection pooling support** (Flight Control uses up to 100 max open connections, 10 max idle connections)

## Database Setup

### 1. Create Database and Users

Connect to your PostgreSQL instance as admin and run:

```sql
-- Create the database
CREATE DATABASE flightctl;

-- Switch to flightctl database to enable extensions
\c flightctl

-- Enable required extensions
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Create users
CREATE USER flightctl_app WITH PASSWORD 'your_app_password';
CREATE USER flightctl_migrator WITH PASSWORD 'your_migration_password';

-- Grant basic privileges
GRANT CONNECT ON DATABASE flightctl TO flightctl_app;
GRANT CONNECT ON DATABASE flightctl TO flightctl_migrator;

-- Grant schema privileges
GRANT USAGE ON SCHEMA public TO flightctl_app;
GRANT USAGE, CREATE ON SCHEMA public TO flightctl_migrator;
GRANT CREATE ON DATABASE flightctl TO flightctl_migrator;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO flightctl_migrator WITH GRANT OPTION;

-- Set up automatic permissions for future tables
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO flightctl_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT USAGE, SELECT ON SEQUENCES TO flightctl_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO flightctl_migrator WITH GRANT OPTION;
```

### 2. Configure SSL (Recommended)

For production deployments, enable SSL on your PostgreSQL instance and configure appropriate connection security.

## Deployment Configuration

### Kubernetes (Helm)

#### 1. Update values.yaml

```yaml
db:
  external: "enabled"
  hostname: "your-postgres-hostname.example.com"
  port: 5432
  name: flightctl
  user: flightctl_app
  migrationUser: flightctl_migrator
  # Set passwords via secrets or values
  userPassword: "your_app_password"
  migrationPassword: "your_migration_password"
  # masterPassword only needed if Flight Control should create users automatically
  # masterPassword: "your_admin_password"  # Optional: for automatic user creation
```

#### 2. Deploy with Helm

```bash
helm install flightctl ./deploy/helm/flightctl \
  --set db.external=enabled \
  --set db.hostname=your-postgres-hostname.example.com \
  --set db.userPassword=your_app_password \
  --set db.migrationPassword=your_migration_password
  # Add masterPassword only if you want Flight Control to create users automatically:
  # --set db.masterPassword=your_admin_password
```

#### 3. Verify Deployment

The following resources will NOT be created when `db.external=enabled`:

- `flightctl-db` Deployment
- `flightctl-db` Service (internal)
- `flightctl-db` PersistentVolumeClaim
- `flightctl-db` ConfigMap

Instead, a `flightctl-db` ExternalName service will be created pointing to your external database.

### Podman Quadlet

#### 1. Update service-config.yaml

Edit `/etc/flightctl/service-config.yaml` and add or update the `db:` section:

```yaml
db:
  external: "enabled"
  hostname: "your-postgres-hostname.example.com"
  port: 5432
  name: flightctl
  user: flightctl_app
  migrationUser: flightctl_migrator
```

**Note**: The `service-config.yaml` file is always required for quadlet deployments (for baseDomain, auth settings, etc.). For internal database deployments, the `db:` section can be omitted entirely or set to `external: "disabled"`.

#### 2. Set up secrets

```bash
# Create password secrets
echo "your_app_password" | podman secret create flightctl-postgresql-user-password -
echo "your_migration_password" | podman secret create flightctl-postgresql-migrator-password -
```

#### 3. Deploy

The quadlet deployment automatically detects external database configuration:

```bash
# Deploy Flight Control (will automatically use external database based on service-config.yaml)
sudo systemctl start flightctl.target
```

The deployment script will:

- **Automatically select** `flightctl-db-external.container` (placeholder container) when `db.external: "enabled"`
- **Skip database readiness checks** since no local PostgreSQL will be running
- **Use regular** `flightctl-db.container` (PostgreSQL container) when `db.external: "disabled"` or omitted

## Migration and Schema Management

Flight Control will automatically:

1. **Validate connectivity** to your external database during startup
2. **Create missing users** using the admin credentials (if `masterPassword` is provided and they don't exist)
3. **Run database migrations** to set up the schema
4. **Set up permissions** for the application user (if admin credentials are provided)

### User Management Options

#### Option 1: Manual User Setup (Recommended for Production)

- Create users manually using the SQL script in the "Database Setup" section
- Only provide `password` and `migrationPassword` in your configuration
- Flight Control will use existing users and skip user creation

#### Option 2: Automatic User Creation

- Provide `masterPassword` (admin credentials) in your configuration
- Flight Control will automatically create missing users and set up permissions
- Useful for development or if you want Flight Control to manage database users

The migration process can use up to three different users:

- **Admin user** (`masterUser`): Creates other users and databases (optional)
- **Migration user** (`migrationUser`): Runs schema migrations and DDL operations (required)
- **Application user** (`user`): Used by Flight Control services for data operations (required)

## Troubleshooting

### Connection Issues

1. **Check network connectivity**:

   ```bash
   # From your Kubernetes cluster or container environment
   telnet your-postgres-hostname.example.com 5432
   ```

2. **Verify PostgreSQL configuration**:
   - Ensure `listen_addresses` includes your Flight Control network
   - Check `pg_hba.conf` for proper authentication rules
   - Verify firewall rules allow connections

3. **Check logs**:

   ```bash
   # Kubernetes
   kubectl logs -f deployment/flightctl-api
   kubectl logs -f job/flightctl-db-migration-<revision>
   
   # Podman
   podman logs flightctl-api
   ```

### Permission Issues

If you see permission errors, ensure:

1. The migration user has CREATE privileges on the database and schema
2. The application user has appropriate data access privileges
3. The admin user can create other users

### SSL/TLS Issues

For SSL connections, you may need to:

1. Add SSL configuration to the database connection string
2. Mount SSL certificates in your containers
3. Configure `sslmode`, `sslcert`, `sslkey`, and `sslrootcert` parameters

## Security Considerations

1. **Use strong passwords** for all database users
2. **Enable SSL/TLS** for database connections in production
3. **Restrict network access** to your database instance
4. **Use secrets management** instead of plain-text passwords in configuration
5. **Regular backup** your external database
6. **Monitor database performance** and set up appropriate alerts

## Backup and Recovery

When using an external database, you are responsible for:

1. **Database backups**: Set up regular automated backups
2. **Point-in-time recovery**: Configure WAL archiving if needed
3. **High availability**: Configure replication or clustering
4. **Monitoring**: Set up database monitoring and alerting
