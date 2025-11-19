# PAM Issuer Deployment Guide

This document describes the deployment configurations for the new `flightctl-pam-issuer` service.

## Overview

The PAM issuer has been separated into its own container with dedicated deployment configurations for Quadlet (Podman/systemd) deployments.

## Deployment Files

### Quadlet (Podman/systemd)

Location: `deploy/podman/flightctl-pam-issuer/`

The following Quadlet files have been created:

1. **flightctl-pam-issuer.container**
   - Quadlet container definition for systemd
   - Manages the PAM issuer container lifecycle
   - Mounts configuration, certificates, and secrets
   - Publishes port 8444
   - Depends on database, KV store, and init services

2. **flightctl-pam-issuer-init.container**
   - Initialization container that templates configuration
   - Runs once before the main container starts
   - Processes `config.yaml.template` and `env.template`

3. **flightctl-pam-issuer-config/config.yaml.template**
   - Template for the PAM issuer configuration
   - Includes placeholders for:
     - Database connection (hostname, port, name, user, SSL config)
     - KV store connection
     - PAM OIDC issuer settings (address, issuer URL, client credentials, scopes, redirect URIs, PAM service)

4. **flightctl-pam-issuer-config/env.template**
   - Environment variable template
   - Sets HOME=/root

5. **flightctl-pam-issuer-config/init.sh**
   - Initialization script executed by the init container
   - Extracts values from `/etc/flightctl/service-config.yaml`
   - Templates configuration files with actual values
   - Handles both internal and external database configurations
   - Converts comma-separated lists to YAML arrays

6. **pam-flightctl**
   - PAM service configuration file
   - Copied to `/etc/pam.d/flightctl` in the container
   - Configures PAM authentication modules

## Service Configuration

The PAM issuer service configuration includes:

- **Session Storage**: In-memory (uses `sync.Map` - does NOT use database or Redis)
- **Authentication**: 
  - Listens on port 8444
  - Provides OIDC-compliant endpoints:
    - `/api/v1/auth/authorize` - OAuth2 authorization endpoint
    - `/api/v1/auth/token` - Token issuance endpoint
    - `/api/v1/auth/userinfo` - User information endpoint
    - `/api/v1/auth/jwks` - JSON Web Key Set endpoint
    - `/api/v1/auth/.well-known/openid-configuration` - OIDC discovery endpoint
  - Uses Linux PAM for user authentication
  - Supports authorization code flow and refresh token grants

## Deployment

```bash
# Copy Quadlet files to systemd directory
sudo cp deploy/podman/flightctl-pam-issuer/*.container /etc/containers/systemd/

# Copy configuration files
sudo mkdir -p /etc/flightctl/flightctl-pam-issuer-config
sudo cp deploy/podman/flightctl-pam-issuer/flightctl-pam-issuer-config/* /etc/flightctl/flightctl-pam-issuer-config/

# Reload systemd
sudo systemctl daemon-reload

# Start the service
sudo systemctl start flightctl-pam-issuer.service
```

## Accessing the Service

The PAM issuer service is accessible at:
- `https://localhost:8444`

## OIDC Discovery

According to the OIDC Discovery specification, the issuer URL must match the base path where the `.well-known/openid-configuration` endpoint is served.

The PAM issuer exposes its OIDC configuration at:

```text
https://localhost:8444/api/v1/auth/.well-known/openid-configuration
```

Therefore, the issuer URL must be configured as:

```text
https://localhost:8444/api/v1/auth
```

This endpoint provides:
- Authorization and token endpoints
- Supported grant types and response types
- JWKS URI for token verification
- Supported scopes and claims

## Security Considerations

1. **TLS**: The PAM issuer uses mTLS for secure communication
2. **PAM Authentication**: User credentials are validated against Linux PAM
3. **Session Management**: Sessions are stored in-memory with expiration
4. **Token Signing**: JWT tokens are signed with RS256 or ES256 keys
5. **Client Authentication**: Supports both public clients (CLI) and confidential clients (backend services)
6. **PKCE Requirement**: By default, PKCE (Proof Key for Code Exchange) is required for public clients per OAuth 2.0 Security Best Current Practice. This can be disabled via the `allowPublicClientWithoutPKCE` configuration option, but this is not recommended for production environments

## Troubleshooting

### Check Container Status

```bash
sudo systemctl status flightctl-pam-issuer.service
sudo journalctl -u flightctl-pam-issuer.service -f
```

### Common Issues

1. **Port Conflicts**: Ensure port 8444 is not already in use
2. **Certificate Errors**: Verify certificates are properly mounted and valid
3. **PAM Configuration**: Ensure `/etc/pam.d/flightctl` exists in the container

## Integration with Main API Server

The main API server can be configured to use the PAM issuer as an external OIDC provider by adding it to the OIDC providers list in the API configuration:

```yaml
auth:
  oidc:
    - clientId: flightctl-client
      enabled: true
      issuer: https://localhost:8444/api/v1/auth
      # ... other OIDC configuration
```

This allows the main API server to validate tokens issued by the PAM issuer without directly depending on PAM libraries.


