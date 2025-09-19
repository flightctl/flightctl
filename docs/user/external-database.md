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

See the [TLS/SSL Configuration](#tlsssl-configuration) section below for detailed setup instructions.

## Deployment Configuration

### Password Management

Flight Control uses a three-tier password precedence system for database credentials:

#### Password Priority Order

1. **values.yaml/values.dev.yaml** (Highest Priority)
2. **Existing Kubernetes Secrets** (Medium Priority)
3. **Auto-generated** (Lowest Priority/Fallback)

#### Password Management Options

##### Option 1: Using values.yaml (Development)

```yaml
db:
  external: "enabled"
  hostname: "your-postgres-hostname.example.com"
  port: 5432
  name: flightctl
  user: flightctl_app
  migrationUser: flightctl_migrator
  userPassword: "your_app_password"
  migrationPassword: "your_migration_password"
  # masterPassword: "your_admin_password"  # Optional: for automatic user creation
```

##### Option 2: Using Kubernetes Secrets (Production)

```bash
# Create secrets before installing Flight Control
kubectl create secret generic flightctl-db-app-secret \
  --from-literal=user=flightctl_app \
  --from-literal=userPassword=your-secure-app-password

kubectl create secret generic flightctl-db-migration-secret \
  --from-literal=migrationUser=flightctl_migrator \
  --from-literal=migrationPassword=your-secure-migration-password

# For automatic user creation (optional)
kubectl create secret generic flightctl-db-admin-secret \
  --from-literal=masterUser=admin \
  --from-literal=masterPassword=your-secure-admin-password
```

Then configure without passwords in values.yaml:

```yaml
db:
  external: "enabled"
  hostname: "your-postgres-hostname.example.com"
  port: 5432
  name: flightctl
  user: flightctl_app
  migrationUser: flightctl_migrator
  # No passwords here - will use existing secrets
```

### Kubernetes (Helm)

#### 1. Configure Database Connection

#### 2. Deploy with Helm

##### Using values.yaml (Development)

```bash
helm install flightctl ./deploy/helm/flightctl \
  --set db.external=enabled \
  --set db.hostname=your-postgres-hostname.example.com \
  --set db.userPassword=your_app_password \
  --set db.migrationPassword=your_migration_password
  # Add masterPassword only if you want Flight Control to create users automatically:
  # --set db.masterPassword=your_admin_password
```

##### Using Kubernetes Secrets (Production)

```bash
# First create secrets (see Password Management section above)
# Then deploy without passwords in command line:
helm install flightctl ./deploy/helm/flightctl \
  --set db.external=enabled \
  --set db.hostname=your-postgres-hostname.example.com
  # Passwords will be automatically discovered from existing secrets
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

## TLS/SSL Configuration

Flight Control supports TLS/SSL connections to external PostgreSQL databases with multiple certificate management options.

### TLS/SSL Parameters

Configure TLS/SSL connection parameters in your deployment configuration:

```yaml
db:
  external: "enabled"
  hostname: "postgres.example.com"
  port: 5432
  sslmode: "require"           # TLS/SSL connection mode
  sslcert: "/etc/ssl/postgres/client-cert.pem"    # Client certificate path
  sslkey: "/etc/ssl/postgres/client-key.pem"      # Client private key path
  sslrootcert: "/etc/ssl/postgres/ca-cert.pem"    # CA certificate path
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
  external: "enabled"
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
sudo cp /path/to/ca-cert.pem /etc/flightctl/ssl/postgres/
sudo cp /path/to/client-cert.pem /etc/flightctl/ssl/postgres/
sudo cp /path/to/client-key.pem /etc/flightctl/ssl/postgres/
sudo chmod 600 /etc/flightctl/ssl/postgres/*-key.pem
sudo chmod 644 /etc/flightctl/ssl/postgres/*-cert.pem
```

##### Step 2: Update service-config.yaml

```yaml
db:
  external: "enabled"
  hostname: "postgres.example.com"
  sslmode: "verify-ca"
  sslcert: "/etc/ssl/postgres/client-cert.pem"
  sslkey: "/etc/ssl/postgres/client-key.pem"
  sslrootcert: "/etc/ssl/postgres/ca-cert.pem"
```

##### Step 3: Update quadlet container files

Add the TLS/SSL certificate volume mount to each database-connected service's `.container` file. The following services connect to the database and require TLS/SSL certificates:

- `deploy/podman/flightctl-api/flightctl-api.container`
- `deploy/podman/flightctl-worker/flightctl-worker.container`
- `deploy/podman/flightctl-periodic/flightctl-periodic.container`
- `deploy/podman/flightctl-db-migrate/flightctl-db-migrate.container`

Add this line to the `[Container]` section of each file:

```ini
Volume=/etc/flightctl/ssl/postgres:/etc/ssl/postgres:ro,Z
```

**Example for flightctl-api.container:**

```ini
[Container]
ContainerName=flightctl-api
Image=quay.io/flightctl/flightctl-api:latest
# ... existing configuration ...
Volume=/etc/flightctl/pki:/root/.flightctl/certs:rw,z
Volume=/etc/flightctl/flightctl-api/config.yaml:/root/.flightctl/config.yaml:ro,z
Volume=/etc/flightctl/ssl/postgres:/etc/ssl/postgres:ro,Z
# ... rest of configuration ...
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

### TLS/SSL Issues

**Common TLS/SSL connection problems:**

1. **Certificate not found errors**:
   - Verify certificate paths match mounted locations (`/etc/ssl/postgres/`)
   - Check that `sslConfigMap` and `sslSecret` are correctly referenced
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
