# PAM Issuer Default Configuration

This document describes the default configuration for the PAM issuer service in different deployment modes.

## Overview

The PAM issuer service has different default configurations based on the deployment method:

- **Quadlet (Podman/systemd)**: PAM issuer is **ENABLED by default**
- **Helm (Kubernetes)**: PAM issuer is **DISABLED by default**

## Quadlet Deployment (Default: ENABLED)

### Configuration

When deploying with `make deploy-quadlets`, the PAM issuer is automatically included and configured to work with the main API server.

**Default Service Configuration** (`deploy/podman/service-config.yaml`):
```yaml
global:
  auth:
    type: oidc
    oidc:
      clientId: flightctl-client
      enabled: true
      # ... other OIDC settings
    pamOidcIssuer:
      enabled: true  # PAM issuer is ENABLED by default
      issuer: # Auto-configured to https://<baseDomain>:8444
      clientId: flightctl-client
      clientSecret:
      scopes: openid,profile,email,roles
      redirectUris:
      pamService: flightctl
```

### What Gets Deployed

The `make deploy-quadlets` target:
1. Builds the PAM issuer container (`build-pam-issuer`)
2. Loads all containers into root podman context, including `flightctl-pam-issuer:latest`
3. Deploys the following services:
   - `flightctl-db` - PostgreSQL database
   - `flightctl-kv` - Redis key-value store
   - `flightctl-pam-issuer` - PAM OIDC issuer (NEW)
   - `flightctl-api` - Main API server (configured to use PAM issuer)
   - `flightctl-worker` - Background worker
   - `flightctl-periodic` - Periodic tasks
   - `flightctl-alert-exporter` - Alert exporter
   - `flightctl-cli-artifacts` - CLI artifacts server
   - `flightctl-alertmanager-proxy` - Alertmanager proxy

### Service Integration

The main API server (`flightctl-api`) is automatically configured to use the PAM issuer as an OIDC provider:

**Generated API Configuration** (when `pamOidcIssuer.enabled: true`):
```yaml
auth:
  oidc:
    - clientId: flightctl-client
      enabled: true
      issuer: <external-oidc-if-configured>
      externalOidcAuthority: <external-oidc-if-configured>
      # ... organization and claim settings
    - clientId: flightctl-client  # PAM issuer as second OIDC provider
      enabled: true
      issuer: https://<baseDomain>:8444
      externalOidcAuthority: https://<baseDomain>:8444
      organizationAssignment:
        type: static
        organizationName: default
      usernameClaim:
        - preferred_username
      roleAssignment:
        type: dynamic
        claimPath:
          - roles
```

This means the API server will:
1. Accept tokens from external OIDC providers (if configured)
2. Accept tokens from the PAM issuer (port 8444)
3. Validate tokens by fetching JWKS from both issuers

### PAM Issuer Configuration

The PAM issuer service is configured as follows:

**Location**: Port 8444  
**Endpoints**:
- `https://<baseDomain>:8444/api/v1/auth/authorize` - Authorization endpoint
- `https://<baseDomain>:8444/api/v1/auth/token` - Token issuance
- `https://<baseDomain>:8444/api/v1/auth/userinfo` - User information
- `https://<baseDomain>:8444/api/v1/auth/jwks` - Public keys
- `https://<baseDomain>:8444/api/v1/auth/.well-known/openid-configuration` - OIDC discovery

**Authentication**: Uses Linux PAM with the `flightctl` service configuration (`/etc/pam.d/flightctl`)

**Session Storage**: In-memory (uses `sync.Map` for authorization codes and sessions)

**User Lookup**: NSS (Name Service Switch) for system user and group information

**Note**: The PAM issuer does NOT use database or Redis. It is completely self-contained with in-memory session management and relies only on PAM/NSS for authentication and user lookups.

### Service Dependencies

The PAM issuer service has the following dependencies (defined in `flightctl-pam-issuer.container`):
```
After=flightctl-pam-issuer-init.service
Wants=flightctl-pam-issuer-init.service
```

This ensures:
1. Configuration is templated correctly before the service starts

**Note**: The PAM issuer does NOT depend on database or Redis services, as it uses in-memory storage.

### Deployment Process

```bash
# Deploy all services including PAM issuer
make deploy-quadlets

# The deploy-quadlets target:
# 1. Builds containers (including PAM issuer)
# 2. Copies containers to root context
# 3. Runs deploy/scripts/deploy_quadlets.sh which:
#    - Installs quadlet files to /etc/containers/systemd/
#    - Switches to locally built images
#    - Ensures secrets exist
#    - Starts flightctl.target
#    - Waits for all services to be ready
```

### Verifying Deployment

```bash
# Check all services status
sudo systemctl status flightctl.target

# Check PAM issuer specifically
sudo systemctl status flightctl-pam-issuer.service

# View PAM issuer logs
sudo journalctl -u flightctl-pam-issuer.service -f

# Test OIDC discovery endpoint
curl -k https://localhost:8444/api/v1/auth/.well-known/openid-configuration
```

## Helm Deployment (Default: DISABLED)

### Configuration

In Helm deployments, the PAM issuer is **disabled by default** in `deploy/helm/flightctl/values.yaml`:

```yaml
pamIssuer:
  enabled: false  # Disabled by default
  image:
    image: quay.io/flightctl/flightctl-pam-issuer
    pullPolicy: ""
    tag: ""
  env: {}
  probes:
    enabled: true
    readinessPath: /api/v1/auth/.well-known/openid-configuration
    livenessPath: /api/v1/auth/.well-known/openid-configuration
```

### Rationale

The PAM issuer is disabled by default in Helm because:
1. **Kubernetes environments** typically have their own identity providers (Keycloak, Dex, OAuth2 Proxy, etc.)
2. **PAM authentication** is less common in containerized Kubernetes environments
3. **External OIDC providers** are the preferred authentication method for production Kubernetes deployments

### Enabling PAM Issuer in Helm

If you want to enable the PAM issuer in a Helm deployment:

```bash
helm upgrade --install flightctl ./deploy/helm/flightctl \
  --set global.auth.type=oidc \
  --set global.auth.oidc.issuer=https://pam-issuer.example.com:8444/api/v1/auth \
  --set global.auth.oidc.clientId=flightctl-client
```

Or in your `values.yaml`:
```yaml
global:
  auth:
    type: oidc
    oidc:
      enabled: true
      issuer: https://pam-issuer.example.com:8444/api/v1/auth
      clientId: flightctl-client
```

Note: The PAM issuer itself is deployed separately (via Podman/systemd) and must be configured with the same issuer URL.

## Disabling PAM Issuer in Quadlet

If you want to disable the PAM issuer in a Quadlet deployment, edit the service configuration:

**File**: `deploy/podman/service-config.yaml`
```yaml
global:
  auth:
    pamOidcIssuer:
      enabled: false  # Change to false
```

Then redeploy:
```bash
make deploy-quadlets
```

This will:
1. Not start the `flightctl-pam-issuer` service
2. Configure the API server without the PAM issuer OIDC provider
3. Rely only on external OIDC providers (if configured)

## Authentication Flow

### With PAM Issuer Enabled (Default for Quadlets)

1. **User Login**:
   - User accesses login form served by PAM issuer
   - Enters username/password
   - PAM issuer validates credentials against Linux PAM
   - Issues authorization code

2. **Token Exchange**:
   - Client exchanges code for tokens at PAM issuer
   - Receives access token, refresh token, and ID token

3. **API Access**:
   - Client sends requests to main API with access token
   - API server validates token using PAM issuer's JWKS
   - Grants access based on token claims

### With PAM Issuer Disabled (Default for Helm)

1. **User Login**:
   - User authenticates with external OIDC provider (e.g., Keycloak)
   - Receives tokens from external provider

2. **API Access**:
   - Client sends requests to main API with access token
   - API server validates token using external provider's JWKS
   - Grants access based on token claims

## Port Mapping

| Service | Quadlet Port | Helm Port | Purpose |
|---------|-------------|-----------|---------|
| Main API | 3443 | 3443 | API endpoints |
| Agent API | 7443 | 7443 | Agent communication |
| PAM Issuer | 8444 | 8444 | OIDC authentication |
| UI | 443 | N/A | Web interface |
| CLI Artifacts | 8090 | 8090 | CLI downloads |

## Security Considerations

### PAM Issuer

1. **Default User**: The PAM issuer container creates a default `flightctl-admin` user with password `flightctl-admin` (must be changed on first login)
2. **TLS**: All communication uses mTLS
3. **Session Storage**: Sessions are stored in-memory (no Redis dependency)
4. **Token Signing**: Uses RS256 or ES256 keys
5. **PAM Configuration**: Uses standard Linux PAM modules (`pam_unix`, `pam_env`)

### API Server

1. **Token Validation**: Validates all tokens against registered OIDC providers
2. **JWKS Caching**: Caches public keys from OIDC providers
3. **Role Mapping**: Maps OIDC claims to Flight Control roles
4. **Organization Assignment**: Supports static or claim-based organization assignment

## Troubleshooting

### PAM Issuer Not Starting

```bash
# Check service status
sudo systemctl status flightctl-pam-issuer.service

# Check logs
sudo journalctl -u flightctl-pam-issuer.service --no-pager -n 100

# Common issues:
# - Port 8444 already in use
# - Database not accessible
# - Redis not accessible
# - Invalid configuration
```

### API Server Not Accepting PAM Issuer Tokens

```bash
# Check API server logs
sudo journalctl -u flightctl-api.service --no-pager -n 100 | grep -i oidc

# Verify OIDC configuration
sudo cat /etc/flightctl/flightctl-api/config/config.yaml | grep -A 20 "oidc:"

# Test JWKS endpoint
curl -k https://localhost:8444/api/v1/auth/jwks
```

### Configuration Not Applied

```bash
# Check if init container ran successfully
sudo systemctl status flightctl-pam-issuer-init.service

# View init logs
sudo journalctl -u flightctl-pam-issuer-init.service --no-pager

# Manually check generated config
sudo cat /etc/flightctl/flightctl-pam-issuer/config/config.yaml
```

## Summary

- **Quadlet (Podman)**: PAM issuer **ENABLED** by default, integrated with main API
- **Helm (Kubernetes)**: PAM issuer **DISABLED** by default, use external OIDC providers
- **Port**: PAM issuer listens on port 8444
- **Integration**: API server automatically configured to validate PAM issuer tokens when enabled
- **Flexibility**: Can be enabled/disabled via configuration without code changes


This document describes the default configuration for the PAM issuer service in different deployment modes.

## Overview

The PAM issuer service has different default configurations based on the deployment method:

- **Quadlet (Podman/systemd)**: PAM issuer is **ENABLED by default**
- **Helm (Kubernetes)**: PAM issuer is **DISABLED by default**

## Quadlet Deployment (Default: ENABLED)

### Configuration

When deploying with `make deploy-quadlets`, the PAM issuer is automatically included and configured to work with the main API server.

**Default Service Configuration** (`deploy/podman/service-config.yaml`):
```yaml
global:
  auth:
    type: oidc
    oidc:
      clientId: flightctl-client
      enabled: true
      # ... other OIDC settings
    pamOidcIssuer:
      enabled: true  # PAM issuer is ENABLED by default
      issuer: # Auto-configured to https://<baseDomain>:8444
      clientId: flightctl-client
      clientSecret:
      scopes: openid,profile,email,roles
      redirectUris:
      pamService: flightctl
```

### What Gets Deployed

The `make deploy-quadlets` target:
1. Builds the PAM issuer container (`build-pam-issuer`)
2. Loads all containers into root podman context, including `flightctl-pam-issuer:latest`
3. Deploys the following services:
   - `flightctl-db` - PostgreSQL database
   - `flightctl-kv` - Redis key-value store
   - `flightctl-pam-issuer` - PAM OIDC issuer (NEW)
   - `flightctl-api` - Main API server (configured to use PAM issuer)
   - `flightctl-worker` - Background worker
   - `flightctl-periodic` - Periodic tasks
   - `flightctl-alert-exporter` - Alert exporter
   - `flightctl-cli-artifacts` - CLI artifacts server
   - `flightctl-alertmanager-proxy` - Alertmanager proxy

### Service Integration

The main API server (`flightctl-api`) is automatically configured to use the PAM issuer as an OIDC provider:

**Generated API Configuration** (when `pamOidcIssuer.enabled: true`):
```yaml
auth:
  oidc:
    - clientId: flightctl-client
      enabled: true
      issuer: <external-oidc-if-configured>
      externalOidcAuthority: <external-oidc-if-configured>
      # ... organization and claim settings
    - clientId: flightctl-client  # PAM issuer as second OIDC provider
      enabled: true
      issuer: https://<baseDomain>:8444
      externalOidcAuthority: https://<baseDomain>:8444
      organizationAssignment:
        type: static
        organizationName: default
      usernameClaim:
        - preferred_username
      roleAssignment:
        type: dynamic
        claimPath:
          - roles
```

This means the API server will:
1. Accept tokens from external OIDC providers (if configured)
2. Accept tokens from the PAM issuer (port 8444)
3. Validate tokens by fetching JWKS from both issuers

### PAM Issuer Configuration

The PAM issuer service is configured as follows:

**Location**: Port 8444  
**Endpoints**:
- `https://<baseDomain>:8444/api/v1/auth/authorize` - Authorization endpoint
- `https://<baseDomain>:8444/api/v1/auth/token` - Token issuance
- `https://<baseDomain>:8444/api/v1/auth/userinfo` - User information
- `https://<baseDomain>:8444/api/v1/auth/jwks` - Public keys
- `https://<baseDomain>:8444/api/v1/auth/.well-known/openid-configuration` - OIDC discovery

**Authentication**: Uses Linux PAM with the `flightctl` service configuration (`/etc/pam.d/flightctl`)

**Session Storage**: In-memory (uses `sync.Map` for authorization codes and sessions)

**User Lookup**: NSS (Name Service Switch) for system user and group information

**Note**: The PAM issuer does NOT use database or Redis. It is completely self-contained with in-memory session management and relies only on PAM/NSS for authentication and user lookups.

### Service Dependencies

The PAM issuer service has the following dependencies (defined in `flightctl-pam-issuer.container`):
```
After=flightctl-pam-issuer-init.service
Wants=flightctl-pam-issuer-init.service
```

This ensures:
1. Configuration is templated correctly before the service starts

**Note**: The PAM issuer does NOT depend on database or Redis services, as it uses in-memory storage.

### Deployment Process

```bash
# Deploy all services including PAM issuer
make deploy-quadlets

# The deploy-quadlets target:
# 1. Builds containers (including PAM issuer)
# 2. Copies containers to root context
# 3. Runs deploy/scripts/deploy_quadlets.sh which:
#    - Installs quadlet files to /etc/containers/systemd/
#    - Switches to locally built images
#    - Ensures secrets exist
#    - Starts flightctl.target
#    - Waits for all services to be ready
```

### Verifying Deployment

```bash
# Check all services status
sudo systemctl status flightctl.target

# Check PAM issuer specifically
sudo systemctl status flightctl-pam-issuer.service

# View PAM issuer logs
sudo journalctl -u flightctl-pam-issuer.service -f

# Test OIDC discovery endpoint
curl -k https://localhost:8444/api/v1/auth/.well-known/openid-configuration
```

## Helm Deployment (Default: DISABLED)

### Configuration

In Helm deployments, the PAM issuer is **disabled by default** in `deploy/helm/flightctl/values.yaml`:

```yaml
pamIssuer:
  enabled: false  # Disabled by default
  image:
    image: quay.io/flightctl/flightctl-pam-issuer
    pullPolicy: ""
    tag: ""
  env: {}
  probes:
    enabled: true
    readinessPath: /api/v1/auth/.well-known/openid-configuration
    livenessPath: /api/v1/auth/.well-known/openid-configuration
```

### Rationale

The PAM issuer is disabled by default in Helm because:
1. **Kubernetes environments** typically have their own identity providers (Keycloak, Dex, OAuth2 Proxy, etc.)
2. **PAM authentication** is less common in containerized Kubernetes environments
3. **External OIDC providers** are the preferred authentication method for production Kubernetes deployments

### Enabling PAM Issuer in Helm

If you want to enable the PAM issuer in a Helm deployment:

```bash
helm upgrade --install flightctl ./deploy/helm/flightctl \
  --set global.auth.type=oidc \
  --set global.auth.oidc.issuer=https://pam-issuer.example.com:8444/api/v1/auth \
  --set global.auth.oidc.clientId=flightctl-client
```

Or in your `values.yaml`:
```yaml
global:
  auth:
    type: oidc
    oidc:
      enabled: true
      issuer: https://pam-issuer.example.com:8444/api/v1/auth
      clientId: flightctl-client
```

Note: The PAM issuer itself is deployed separately (via Podman/systemd) and must be configured with the same issuer URL.

## Disabling PAM Issuer in Quadlet

If you want to disable the PAM issuer in a Quadlet deployment, edit the service configuration:

**File**: `deploy/podman/service-config.yaml`
```yaml
global:
  auth:
    pamOidcIssuer:
      enabled: false  # Change to false
```

Then redeploy:
```bash
make deploy-quadlets
```

This will:
1. Not start the `flightctl-pam-issuer` service
2. Configure the API server without the PAM issuer OIDC provider
3. Rely only on external OIDC providers (if configured)

## Authentication Flow

### With PAM Issuer Enabled (Default for Quadlets)

1. **User Login**:
   - User accesses login form served by PAM issuer
   - Enters username/password
   - PAM issuer validates credentials against Linux PAM
   - Issues authorization code

2. **Token Exchange**:
   - Client exchanges code for tokens at PAM issuer
   - Receives access token, refresh token, and ID token

3. **API Access**:
   - Client sends requests to main API with access token
   - API server validates token using PAM issuer's JWKS
   - Grants access based on token claims

### With PAM Issuer Disabled (Default for Helm)

1. **User Login**:
   - User authenticates with external OIDC provider (e.g., Keycloak)
   - Receives tokens from external provider

2. **API Access**:
   - Client sends requests to main API with access token
   - API server validates token using external provider's JWKS
   - Grants access based on token claims

## Port Mapping

| Service | Quadlet Port | Helm Port | Purpose |
|---------|-------------|-----------|---------|
| Main API | 3443 | 3443 | API endpoints |
| Agent API | 7443 | 7443 | Agent communication |
| PAM Issuer | 8444 | 8444 | OIDC authentication |
| UI | 443 | N/A | Web interface |
| CLI Artifacts | 8090 | 8090 | CLI downloads |

## Security Considerations

### PAM Issuer

1. **Default User**: The PAM issuer container creates a default `flightctl-admin` user with password `flightctl-admin` (must be changed on first login)
2. **TLS**: All communication uses mTLS
3. **Session Storage**: Sessions are stored in-memory (no Redis dependency)
4. **Token Signing**: Uses RS256 or ES256 keys
5. **PAM Configuration**: Uses standard Linux PAM modules (`pam_unix`, `pam_env`)

### API Server

1. **Token Validation**: Validates all tokens against registered OIDC providers
2. **JWKS Caching**: Caches public keys from OIDC providers
3. **Role Mapping**: Maps OIDC claims to Flight Control roles
4. **Organization Assignment**: Supports static or claim-based organization assignment

## Troubleshooting

### PAM Issuer Not Starting

```bash
# Check service status
sudo systemctl status flightctl-pam-issuer.service

# Check logs
sudo journalctl -u flightctl-pam-issuer.service --no-pager -n 100

# Common issues:
# - Port 8444 already in use
# - Database not accessible
# - Redis not accessible
# - Invalid configuration
```

### API Server Not Accepting PAM Issuer Tokens

```bash
# Check API server logs
sudo journalctl -u flightctl-api.service --no-pager -n 100 | grep -i oidc

# Verify OIDC configuration
sudo cat /etc/flightctl/flightctl-api/config/config.yaml | grep -A 20 "oidc:"

# Test JWKS endpoint
curl -k https://localhost:8444/api/v1/auth/jwks
```

### Configuration Not Applied

```bash
# Check if init container ran successfully
sudo systemctl status flightctl-pam-issuer-init.service

# View init logs
sudo journalctl -u flightctl-pam-issuer-init.service --no-pager

# Manually check generated config
sudo cat /etc/flightctl/flightctl-pam-issuer/config/config.yaml
```

## Summary

- **Quadlet (Podman)**: PAM issuer **ENABLED** by default, integrated with main API
- **Helm (Kubernetes)**: PAM issuer **DISABLED** by default, use external OIDC providers
- **Port**: PAM issuer listens on port 8444
- **Integration**: API server automatically configured to validate PAM issuer tokens when enabled
- **Flexibility**: Can be enabled/disabled via configuration without code changes

