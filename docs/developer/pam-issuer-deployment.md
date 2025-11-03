# PAM Issuer Deployment Guide

This document describes the deployment configurations for the new `flightctl-pam-issuer` service.

## Overview

The PAM issuer has been separated into its own container with dedicated deployment configurations for both Helm (Kubernetes) and Quadlet (Podman/systemd) deployments.

## Deployment Files

### Helm (Kubernetes) Deployment

Location: `deploy/helm/flightctl/templates/pam-issuer/`

The following Helm templates have been created:

1. **flightctl-pam-issuer-deployment.yaml**
   - Kubernetes Deployment resource for the PAM issuer
   - Configures container with environment variables, volumes, and probes
   - No database or Redis dependencies (uses in-memory storage)
   - Exposes port 8444 for OIDC endpoints

2. **flightctl-pam-issuer-service.yaml**
   - Kubernetes Service resource
   - Exposes the PAM issuer on port 8444
   - Supports NodePort configuration via values

3. **flightctl-pam-issuer-config.yaml**
   - ConfigMap containing the PAM issuer configuration
   - Templates database connection, KV store, and PAM OIDC settings
   - Automatically configured based on Helm values

4. **flightctl-pam-issuer-certs-persistentvolumeclaim.yaml**
   - PVC for storing TLS certificates
   - 100Mi storage for certificate management

5. **flightctl-pam-issuer-serviceaccount.yaml**
   - Service account for RBAC configuration

6. **flightctl-pam-issuer-route.yaml**
   - OpenShift Route for exposing the service (route mode)
   - Configured for passthrough TLS termination
   - Hostname: `pam-issuer.<baseDomain>`

7. **flightctl-pam-issuer-gateway-route.yaml**
   - Gateway API HTTPRoute for exposing the service (gateway mode)
   - Alternative to OpenShift Route for Kubernetes Gateway API

### Helm Values Configuration

Add the following to your `values.yaml`:

```yaml
global:
  nodePorts:
    pamIssuer: 8444  # NodePort for PAM issuer service

  auth:
    pamOidcIssuer:
      issuer: ""  # Base URL for the OIDC issuer (must include /api/v1/auth path, e.g., https://pam-issuer.example.com:8444/api/v1/auth)
      clientId: "flightctl-client"
      clientSecret: ""
      scopes: ["openid", "profile", "email", "roles"]
      redirectUris: []
      pamService: "flightctl"

pamIssuer:
  enabled: true
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

### Quadlet (Podman/systemd) Deployment

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

### Helm Deployment

```bash
# Install/upgrade with Helm
helm upgrade --install flightctl ./deploy/helm/flightctl \
  --set pamIssuer.enabled=true \
  --set global.auth.pamOidcIssuer.clientId=flightctl-client \
  --set global.auth.pamOidcIssuer.clientSecret=<your-secret>
```

### Quadlet Deployment

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

### Kubernetes/OpenShift
- Route mode: `https://pam-issuer.<baseDomain>`
- NodePort mode: `https://<node-ip>:8444`
- Gateway mode: `https://pam-issuer.<baseDomain>` (via Gateway API)

### Podman
- Local: `https://localhost:8444`

## OIDC Discovery

According to the OIDC Discovery specification, the issuer URL must match the base path where the `.well-known/openid-configuration` endpoint is served.

The PAM issuer exposes its OIDC configuration at:

```text
https://pam-issuer.<baseDomain>:8444/api/v1/auth/.well-known/openid-configuration
```

Therefore, the issuer URL must be configured as:

```text
https://pam-issuer.<baseDomain>:8444/api/v1/auth
```

This endpoint provides:
- Authorization and token endpoints
- Supported grant types and response types
- JWKS URI for token verification
- Supported scopes and claims

## Security Considerations

1. **TLS**: The PAM issuer uses mTLS for secure communication
2. **PAM Authentication**: User credentials are validated against Linux PAM
3. **Session Management**: Sessions are stored in Redis with expiration
4. **Token Signing**: JWT tokens are signed with RS256 or ES256 keys
5. **Client Authentication**: Supports both public clients (CLI) and confidential clients (backend services)

## Troubleshooting

### Check Pod/Container Status

Kubernetes:
```bash
kubectl get pods -l flightctl.service=flightctl-pam-issuer
kubectl logs -l flightctl.service=flightctl-pam-issuer
```

Podman:
```bash
sudo systemctl status flightctl-pam-issuer.service
sudo journalctl -u flightctl-pam-issuer.service -f
```

### Common Issues

1. **Port Conflicts**: Ensure port 8444 is not already in use
2. **Certificate Errors**: Verify certificates are properly mounted and valid
3. **Database Connection**: Check database credentials and connectivity
4. **PAM Configuration**: Ensure `/etc/pam.d/flightctl` exists in the container
5. **KV Store**: Verify Redis is running and accessible

## Integration with Main API Server

The main API server can be configured to use the PAM issuer as an external OIDC provider by adding it to the OIDC providers list in the API configuration:

```yaml
auth:
  oidc:
    - clientId: flightctl-client
      enabled: true
      issuer: https://pam-issuer.<baseDomain>:8444/api/v1/auth
      # ... other OIDC configuration
```

This allows the main API server to validate tokens issued by the PAM issuer without directly depending on PAM libraries.


