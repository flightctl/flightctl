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

-- Grant permissions on existing tables/sequences (for any pre-existing objects)
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO flightctl_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO flightctl_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO flightctl_migrator;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO flightctl_migrator;

-- Create function to grant permissions on existing tables (called after migrations)
CREATE OR REPLACE FUNCTION grant_app_permissions_on_existing_tables()
RETURNS void AS $$
BEGIN
    -- Grant table permissions
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I', 'flightctl_app');
    -- Grant sequence permissions
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %I', 'flightctl_app');
END;
$$ LANGUAGE plpgsql;

-- Create event trigger function for automatic permission granting
CREATE OR REPLACE FUNCTION grant_app_permissions()
RETURNS event_trigger AS $$
BEGIN
    -- Grant permissions on newly created tables/sequences
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I', 'flightctl_app');
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %I', 'flightctl_app');
END;
$$ LANGUAGE plpgsql;

-- Set up automatic permission granting for future table creation
DROP EVENT TRIGGER IF EXISTS grant_app_permissions_trigger;
CREATE EVENT TRIGGER grant_app_permissions_trigger
    ON ddl_command_end
    WHEN TAG IN ('CREATE TABLE', 'CREATE SEQUENCE')
    EXECUTE FUNCTION grant_app_permissions();
```

**Important**: The automatic permission management functions and event trigger above ensure that `flightctl_app` user automatically receives proper permissions on all tables and sequences created by the migration process. This replicates the same automatic permission handling that Flight Control provides for internal databases.

### 2. Configure SSL (Recommended)

For production deployments, enable SSL on your PostgreSQL instance and configure appropriate connection security.

See the [TLS/SSL Configuration](#tlsssl-configuration) section below for detailed setup instructions.

## Deployment Configuration

### Password Management

Flight Control uses secure password management through secrets infrastructure. Password management differs between deployment methods:

#### Kubernetes Deployment

Flight Control uses a three-tier password precedence system for database credentials:

##### Password Priority Order

1. **values.yaml/values.dev.yaml** (Highest Priority)
2. **Existing Kubernetes Secrets** (Medium Priority)
3. **Auto-generated** (Lowest Priority/Fallback)

#### Podman Quadlet Deployment

Flight Control uses **Podman secrets exclusively** for all database passwords (both internal and external):

1. **Existing Podman Secrets** (Highest Priority)
2. **Auto-generated** (Fallback for internal databases only)

**Important**: For external databases with Podman deployment, you **must** create Podman secrets manually before installation.

#### Password Management

##### Using Kubernetes Secrets

```bash
# Create database secrets with proper Helm ownership metadata
kubectl create secret generic flightctl-db-app-secret \
  --from-literal=user=flightctl_app \
  --from-literal=userPassword=your-secure-app-password \
  -n flightctl

kubectl create secret generic flightctl-db-migration-secret \
  --from-literal=migrationUser=flightctl_migrator \
  --from-literal=migrationPassword=your-secure-migration-password \
  -n flightctl

# For automatic user creation (optional)
kubectl create secret generic flightctl-db-admin-secret \
  --from-literal=masterUser=admin \
  --from-literal=masterPassword=your-secure-admin-password \
  -n flightctl

# Add the required Helm ownership metadata
kubectl label secret flightctl-db-app-secret flightctl-db-migration-secret flightctl-db-admin-secret \
  app.kubernetes.io/managed-by=Helm \
  -n flightctl

kubectl annotate secret flightctl-db-app-secret flightctl-db-migration-secret flightctl-db-admin-secret \
  meta.helm.sh/release-name=flightctl \
  meta.helm.sh/release-namespace=flightctl \
  -n flightctl
```

Then configure without passwords in values.yaml:

```yaml
db:
  type: "external"
  name: flightctl
  masterUserSecretName: flightctl-db-admin-secret
  applicationUserSecretName: flightctl-db-app-secret
  migrationUserSecretName: flightctl-db-migration-secret
  external:
    hostname: "your-postgres-hostname.example.com"
    port: 5432
```

### Kubernetes (Helm)

#### 1. Configure Database Connection

#### 2. Deploy with Helm

##### Using values.yaml (Development)

```bash
helm install flightctl ./deploy/helm/flightctl \
  --set db.type=external \
  --set db.external.hostname=your-postgres-hostname.example.com \
  --set db.userPassword=your_app_password \
  --set db.migrationPassword=your_migration_password
  # Add masterPassword only if you want Flight Control to create users automatically:
  # --set db.masterPassword=your_admin_password
```

##### Using Kubernetes Secrets (Production)

```bash
# First create secrets (see Password Management section above)
# Then deploy - Helm will automatically detect existing database secrets:
helm install flightctl ./deploy/helm/flightctl \
  --set db.type=external \
  --set db.external.hostname=your-postgres-hostname.example.com \
  --set db.external.sslrootcert="/etc/ssl/postgres/ca-cert.pem"
  # Passwords will be automatically discovered from existing secrets
  # Other required secrets (KV, etc.) will be generated automatically
```

**Note**: The Helm chart automatically detects existing database secrets and skips creating them, while still generating other required internal secrets.

#### 3. Verify Deployment

The following resources will NOT be created when `db.type=external`:

- `flightctl-db` Deployment
- `flightctl-db` Service (internal)
- `flightctl-db` PersistentVolumeClaim
- `flightctl-db` ConfigMap

Instead, a `flightctl-db` ExternalName service will be created pointing to your external database.

**Verification Commands:**

```bash
# Check all services are running
kubectl get pods -n flightctl

# Verify no internal database deployment exists
kubectl get deployment flightctl-db -n flightctl
# Should return: Error from server (NotFound)

# Check migration job completed successfully
kubectl get jobs -n flightctl
kubectl logs job/flightctl-db-migration-<revision> -n flightctl
```

**Common Issues During Deployment:**

1. **Connection Issues**:

   ```bash
   # Check network connectivity from cluster
   kubectl run test-connection --image=postgres:16 --rm -it -- \
     psql -h your-postgres-hostname.example.com -p 5432 -U flightctl_app -d flightctl -c "SELECT 1"
   ```

2. **Check service logs**:

   ```bash
   kubectl logs -f deployment/flightctl-api -n flightctl
   kubectl logs -f job/flightctl-db-migration-<revision> -n flightctl
   ```

3. **Permission Issues**: Ensure the migration user has CREATE privileges and application user has data access privileges

### Podman Quadlet

#### 1. Update service-config.yaml

Edit `/etc/flightctl/service-config.yaml` and add or update the `db:` section:

```yaml
db:
  type: "external"
  external:
    hostname: "your-postgres-hostname.example.com"
    port: 5432
    name: flightctl
  # Note: Passwords are managed through Podman secrets, not YAML config
```

**Note**: The `service-config.yaml` file is always required for quadlet deployments (for baseDomain, auth settings, etc.). For internal database deployments, the `db:` section can be omitted entirely (defaults to `type: "builtin"`). **Passwords are never stored in YAML files** - they are managed through Podman secrets for security.

#### 2. Set up secrets

```bash
# Create password secrets
echo -n "your_app_password" | podman secret create flightctl-postgresql-user-password -
echo -n "your_migration_password" | podman secret create flightctl-postgresql-migrator-password -
```

#### 3. Deploy

After RPM installation, configure and deploy Flight Control with external database:

```bash
# 1. Configure external database connection
sudo vi /etc/flightctl/service-config.yaml
# Set: type: "external" and configure the external: block with your database details

# 2. Disable internal database services (they conflict with external database)
sudo systemctl mask flightctl-db.service flightctl-db-users-init.service

# 3. Deploy Flight Control services
sudo systemctl start flightctl.target

# 4. Verify external database connection
sudo systemctl status flightctl-db-wait.service  # Should succeed connecting to external DB
sudo systemctl status flightctl-db-migrate.service  # Should complete schema migration
sudo podman ps -a  # Should show services but NO flightctl-db container
```

**Deployment Steps Explained:**

1. **Service Configuration**: Edit `/etc/flightctl/service-config.yaml` (installed by RPM) to specify external database connection details
2. **Disable Internal Database**: Use `systemctl mask` to prevent internal PostgreSQL container from starting
3. **Migration and Wait Services**: These remain enabled and will:
   - Wait for your external database to be ready (`flightctl-db-wait.service`)
   - Run schema migrations against your external database (`flightctl-db-migrate.service`)
4. **Service Startup**: All Flight Control services connect to your external database

**Configuration Requirements:**

- **Password Management**:
  - **Both internal and external databases**: Passwords managed securely through Podman secrets
  - **Internal databases**: Passwords auto-generated if secrets don't exist
  - **External databases**: Users must create Podman secrets manually before deployment
  - **Security**: Passwords are never stored in YAML configuration files
- **SSL Certificate Setup**: SSL certificates are not auto-mounted and must be manually placed on the host at `/etc/flightctl/pki/` with paths specified in service-config.yaml
- **Service Dependencies**: Migration and database wait services still run for external databases to ensure schema setup and connectivity
- **Configuration Precedence**: Environment variables always take precedence over config file values, allowing flexible overrides without modifying files

#### 4. Verify Deployment

**Check all services are running properly:**

```bash
# Should show services but NO internal database
sudo podman ps -a | grep flightctl

# All services should be active/running
sudo systemctl list-units 'flightctl*' --state=active

# Database migration should have completed successfully
sudo systemctl status flightctl-db-migrate.service
```

**Common Issues During Quadlet Deployment:**

1. **Internal Database Container Still Running**:

   **Problem**: After configuring external database, you still see `flightctl-db` container running:

   ```bash
   sudo podman ps -a
   # Shows: flightctl-db container running (should not be present)
   ```

   **Solution**: Disable the internal database services that conflict with external database:

   ```bash
   # Stop all services
   sudo systemctl stop flightctl.target

   # Disable internal database services
   sudo systemctl mask flightctl-db.service flightctl-db-users-init.service

   # Restart services
   sudo systemctl start flightctl.target

   # Verify - should show NO flightctl-db container
   sudo podman ps -a
   ```

2. **Migration or Connection Failures**:

   **Problem**: Services fail to connect to external database or migrations fail.

   **Diagnosis**:

   ```bash
   # Check database wait service (tests connectivity)
   sudo systemctl status flightctl-db-wait.service
   sudo journalctl -u flightctl-db-wait.service

   # Check migration service (runs schema setup)
   sudo systemctl status flightctl-db-migrate.service
   sudo journalctl -u flightctl-db-migrate.service

   # Check API service logs for database connection errors
   sudo journalctl -u flightctl-api.service | grep -i database
   ```

   **Common Causes**:
   - **Network connectivity**: Verify firewall rules and network routing to external database
   - **Authentication**: Check Podman secrets and usernames in service-config.yaml
   - **SSL configuration**: Verify SSL certificates and connection parameters
   - **Database permissions**: Ensure migration user has sufficient privileges

3. **Password Authentication Issues**:

   **Problem**: Migration service fails with "password authentication failed"

   **Solution**: Check and fix Podman secrets:

   ```bash
   # Check actual secret contents
   sudo podman secret inspect flightctl-postgresql-migrator-password --showsecret
   sudo podman secret inspect flightctl-postgresql-user-password --showsecret

   # If passwords don't match your database, recreate secrets:
   sudo podman secret rm flightctl-postgresql-migrator-password
   echo -n "your_migration_password" | sudo podman secret create flightctl-postgresql-migrator-password -

   # Restart migration service
   sudo systemctl restart flightctl-db-migrate.service
   ```

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

## TLS/SSL Configuration

Flight Control supports TLS/SSL connections to external PostgreSQL databases with multiple certificate management options.

### TLS/SSL Parameters

Configure TLS/SSL connection parameters in your deployment configuration:

```yaml
db:
  type: "external"
  external:
     hostname: "postgres.example.com"
     port: 5432
     sslmode: "require"       # TLS/SSL connection mode. If verify-ca or verify-full, user has to create the DB ca.crt at /etc/flightctl/pki/db/ca.crt
     useClientCertAuth: true  # If true, user has to create /etc/flightctl/pki/db/client.crt and /etc/flightctl/pki/db/client.key
```

**TLS/SSL Modes:**

- `disable` - No TLS/SSL (not recommended for production)
- `allow` - TLS/SSL if available, otherwise plain connection
- `prefer` - TLS/SSL preferred, fallback to plain connection
- `require` - TLS/SSL required, no certificate verification
- `verify-ca` - TLS/SSL required, verify server certificate against CA
- `verify-full` - TLS/SSL required, verify certificate and hostname

### Certificate Management Options

#### Option 1: Kubernetes ConfigMap/Secret (Production)

##### Step 1: Create certificate resources

```bash
# Create ConfigMap for CA certificate (public)
kubectl create configmap postgres-ca-cert \
  --from-file=ca-cert.pem=/path/to/ca-cert.pem

# Create Secret for client certificates (private)
kubectl create secret generic postgres-client-certs \
  --from-file=client-cert.pem=/path/to/client-cert.pem \
  --from-file=client-key.pem=/path/to/client-key.pem
```

##### Step 2: Configure Helm values

```yaml
db:
  type: "external"
  external:
    hostname: "postgres.example.com"
    sslmode: "verify-ca"
    # Reference the certificate resources
    sslConfigMap: "postgres-ca-cert"     # ConfigMap containing CA certificate
    sslSecret: "postgres-client-certs"   # Secret containing client certificates
```

The certificates will be automatically mounted at `/etc/ssl/postgres/` in all database-connected services.

#### Option 2: Host Volume Mount (Development/Quadlet)

##### Step 1: Store certificates on host

```bash
sudo mkdir -p /etc/flightctl/ssl/postgres
sudo cp /path/to/ca-cert.pem /etc/flightctl/pki/db/ca.crt
sudo cp /path/to/client-cert.pem /etc/flightctl/pki/db/client.crt
sudo cp /path/to/client-key.pem /etc/flightctl/pki/db/client.key
sudo chmod 600 /etc/flightctl/pki/db/*.key
sudo chmod 644 /etc/flightctl/pki/db/*.crt
```

##### Step 2: Update service-config.yaml

```yaml
db:
  type: "external"
  external:
    hostname: "postgres.example.com"
    sslmode: "verify-ca"
    useClientCertAuth: true
```

### TLS/SSL Certificate Generation Example

For testing purposes, you can generate self-signed certificates:

```bash
# Create certificate directory
mkdir -p ~/postgres-ssl && cd ~/postgres-ssl

# Generate CA private key and certificate
openssl genrsa -out ca-key.pem 2048
openssl req -new -x509 -key ca-key.pem -out ca-cert.pem -days 365 \
  -subj "/C=US/ST=State/L=City/O=Org/CN=PostgreSQL-CA"

# Generate server private key and certificate
openssl genrsa -out server-key.pem 2048
openssl req -new -key server-key.pem -out server-req.pem \
  -subj "/C=US/ST=State/L=City/O=Org/CN=postgres.example.com"
openssl x509 -req -in server-req.pem -CA ca-cert.pem -CAkey ca-key.pem \
  -out server-cert.pem -days 365 -CAcreateserial

# Generate client private key and certificate
openssl genrsa -out client-key.pem 2048
openssl req -new -key client-key.pem -out client-req.pem \
  -subj "/C=US/ST=State/L=City/O=Org/CN=flightctl-client"
openssl x509 -req -in client-req.pem -CA ca-cert.pem -CAkey ca-key.pem \
  -out client-cert.pem -days 365 -CAcreateserial

# Set proper permissions
chmod 600 *-key.pem
chmod 644 *-cert.pem ca-cert.pem
```

### Testing TLS/SSL Connections

Verify TLS/SSL connectivity to your PostgreSQL instance:

```bash
# Test with psql client
PGPASSWORD=your_password psql \
  -h postgres.example.com \
  -p 5432 \
  -U flightctl_app \
  -d flightctl \
  -c "SELECT ssl_is_used();" \
  --set=sslmode=verify-ca \
  --set=sslcert=/path/to/client-cert.pem \
  --set=sslkey=/path/to/client-key.pem \
  --set=sslrootcert=/path/to/ca-cert.pem
```

### TLS/SSL Issues

**Common TLS/SSL connection problems during deployment:**

1. **Certificate not found errors**:
   - Ensure certificates are properly mounted in all database-connected services

2. **TLS/SSL verification failures**:
   - Use `sslmode: require` to skip certificate verification for testing
   - Verify CA certificate contains the correct certificate chain
   - Check that server certificate CN/SAN matches the hostname

3. **Permission denied errors**:
   - Ensure private key files have correct permissions (600)
   - Verify Kubernetes secrets are properly created
   - Check that service accounts have access to certificate resources

## Security Considerations

### Password Security

1. **Production**: Use Kubernetes secrets instead of values.yaml for passwords
2. **Development**: values.yaml is acceptable for convenience but not for production
3. **Strong passwords**: Use complex passwords for all database users
4. **Password rotation**: Regularly rotate database passwords
5. **Separate passwords**: Use different passwords for each user (admin, migration, application)

### Database Security

1. **Enable TLS/SSL** for database connections in production
2. **Restrict network access** to your database instance
3. **Use secrets management** instead of plain-text passwords in configuration
4. **Regular backup** your external database
5. **Monitor database performance** and set up appropriate alerts

### Password Visibility Warning

**Important**: Passwords set in values.yaml are visible via `helm get values` command. For production deployments, always use Kubernetes secrets to protect sensitive credentials.

## Backup and Recovery

When using an external database, you are responsible for:

1. **Database backups**: Set up regular automated backups
2. **Point-in-time recovery**: Configure WAL archiving if needed
3. **High availability**: Configure replication or clustering
4. **Monitoring**: Set up database monitoring and alerting
