# PAM Issuer Service Architecture

## Overview

The PAM issuer service has been separated into its own independent microservice to isolate PAM library dependencies from the main API server. This allows the main API server to be built and deployed without any PAM dependencies, improving portability and security.

## Architecture

```
┌─────────────────────────┐         ┌──────────────────────────┐
│  Main API Server        │         │  PAM Issuer Service      │
│  (flightctl-api)        │         │  (flightctl-pam-issuer)  │
│  Port: 3443 (default)   │         │  Port: 8444 (default)    │
│                         │         │                          │
│  - Resource Management  │         │  - PAM Authentication    │
│  - Token Validation     │◄────────│  - OAuth2/OIDC Flows     │
│  - NO PAM Dependency    │  JWTs   │  - JWT Issuance          │
│                         │         │  - HAS PAM Dependency    │
└─────────────────────────┘         └──────────────────────────┘
```

## Components

### 1. PAM Issuer Service (`cmd/flightctl-pam-issuer`)

**Purpose**: Standalone OIDC/OAuth2 issuer service that authenticates users via PAM

**Key Features**:
- Linux-only (uses `//go:build linux` tag)
- Implements full OAuth2/OIDC authorization code flow
- Provides endpoints:
  - `GET  /.well-known/openid-configuration` - OIDC discovery
  - `GET  /api/v1/auth/authorize` - Authorization endpoint
  - `GET  /api/v1/auth/login` - Login form
  - `POST /api/v1/auth/login` - Login submission
  - `POST /api/v1/auth/token` - Token endpoint
  - `GET  /api/v1/auth/userinfo` - UserInfo endpoint
  - `GET  /api/v1/auth/jwks` - JWKS endpoint

**Dependencies**:
- Requires PAM library (`github.com/msteinert/pam/v2`)
- Must run on Linux
- Requires access to system user database (PAM)

**Build**:
```bash
make build-pam-issuer          # Build binary
make flightctl-pam-issuer-container  # Build container
```

### 2. Main API Server (`cmd/flightctl-api`)

**Purpose**: Core API server for device management

**Key Changes**:
- Removed direct PAM OIDC provider instantiation
- No longer has PAM library dependency
- Can be built on any platform
- Validates JWTs from external OIDC providers (including PAM issuer)

**Dependencies**:
- NO PAM library required
- Can run on Linux, macOS, Windows

**Build**:
```bash
make build-api          # Build binary (any platform)
make flightctl-api-container  # Build container
```

### 3. API Package (`api/v1alpha1/pam-issuer`)

**Purpose**: OpenAPI spec and generated types for PAM issuer

**Contents**:
- `openapi.yaml` - PAM issuer API specification
- `types.gen.go` - Generated types
- `spec.gen.go` - Generated server stubs

**Generation**:
```bash
cd api/v1alpha1/pam-issuer && go generate
```

### 4. Server Package (`internal/pam_issuer_server`)

**Purpose**: HTTP handlers for PAM issuer service

**Files**:
- `server.go` - Server initialization and routing
- `handler.go` - Request handlers (with Linux build tag)

## Configuration

Add the PAM issuer configuration to your config file:

```yaml
auth:
  pamOIDCIssuer:
    address: ":8444"  # Listen address for PAM issuer service
    issuer: "https://flightctl.example.com"  # Base URL for OIDC issuer
    clientId: "flightctl"
    clientSecret: ""  # Empty for public clients (CLI)
    scopes:
      - "openid"
      - "profile"
      - "email"
      - "roles"
      - "offline_access"
    redirectUris:
      - "http://localhost:7777/callback"
    pamService: "flightctl"  # PAM service name
```

## Deployment

### Development

Run both services locally:

```bash
# Terminal 1: Start main API server
./bin/flightctl-api

# Terminal 2: Start PAM issuer service
./bin/flightctl-pam-issuer
```

### Container Deployment

```bash
# Build containers
make flightctl-api-container
make flightctl-pam-issuer-container

# Run with podman
podman run -d --name flightctl-api -p 3443:3443 flightctl-api:latest
podman run -d --name flightctl-pam-issuer -p 8444:8444 flightctl-pam-issuer:latest
```

### Kubernetes Deployment

Deploy as separate services:

```yaml
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: flightctl-pam-issuer
spec:
  replicas: 1
  selector:
    matchLabels:
      app: flightctl-pam-issuer
  template:
    metadata:
      labels:
        app: flightctl-pam-issuer
    spec:
      containers:
      - name: pam-issuer
        image: flightctl-pam-issuer:latest
        ports:
        - containerPort: 8444
        volumeMounts:
        - name: user-db
          mountPath: /etc/passwd
        - name: shadow-db
          mountPath: /etc/shadow
        - name: group-db
          mountPath: /etc/group
      volumes:
      - name: user-db
        hostPath:
          path: /etc/passwd
      - name: shadow-db
        hostPath:
          path: /etc/shadow
      - name: group-db
        hostPath:
          path: /etc/group
---
apiVersion: v1
kind: Service
metadata:
  name: flightctl-pam-issuer
spec:
  selector:
    app: flightctl-pam-issuer
  ports:
  - port: 8444
    targetPort: 8444
```

## Benefits

### 1. **Dependency Isolation**
- Main API server has NO PAM dependencies
- Can be built on any platform (Linux, macOS, Windows)
- Smaller attack surface for the main API

### 2. **Platform Flexibility**
- Main API server is platform-agnostic
- Only PAM issuer requires Linux
- Easier to develop and test on non-Linux platforms

### 3. **Security**
- PAM credentials only handled by dedicated service
- Main API only validates tokens (doesn't need PAM access)
- Can deploy PAM issuer in restricted environment

### 4. **Scalability**
- Services can be scaled independently
- PAM issuer can handle auth load separately
- Main API focuses on resource management

### 5. **Flexibility**
- Easy to add other authentication methods
- Can run without PAM issuer (using other OIDC providers)
- PAM issuer can be swapped with other auth services

## Migration Notes

### For Developers

**Before**:
```go
// Main API server had PAM dependency
pamProvider, err := issuer.NewPAMOIDCProvider(ca, config)
```

**After**:
```go
// Main API server has no OIDC issuer
// Auth endpoints are served by separate PAM issuer service
serviceHandler := service.NewServiceHandler(..., nil)
```

### For Deployments

**Before**:
- Single `flightctl-api` container with PAM library

**After**:
- `flightctl-api` container (no PAM)
- `flightctl-pam-issuer` container (with PAM)

### For CLI Users

Update CLI to point to PAM issuer:

```bash
flightctl login --oidc-issuer=https://pam-issuer.example.com
```

## Testing

### Unit Tests
```bash
# Test main API (no PAM required)
go test ./internal/api_server/...

# Test PAM issuer (requires Linux)
go test -tags linux ./internal/pam_issuer_server/...
```

### Integration Tests
```bash
# Start both services
./bin/flightctl-api &
./bin/flightctl-pam-issuer &

# Run tests
go test ./test/integration/...
```

## Troubleshooting

### PAM Issuer Won't Start

**Issue**: `failed to create PAM OIDC provider`

**Solution**: Ensure:
1. Running on Linux
2. PAM configuration exists (`/etc/pam.d/flightctl`)
3. User database is accessible

### Token Validation Fails

**Issue**: Main API can't validate tokens

**Solution**: Ensure:
1. PAM issuer JWKS endpoint is accessible
2. Issuer URL matches in both services
3. Network connectivity between services

### Build Fails on Non-Linux

**Issue**: PAM library not found during build

**Solution**:
- Don't build `flightctl-pam-issuer` on non-Linux
- Only build `flightctl-api` (no PAM dependency)
