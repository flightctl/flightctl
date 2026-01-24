# Flight Control Standalone Observability Stack

This comprehensive guide covers the complete Flight Control observability stack, including installation, configuration, management, and troubleshooting.

## Table of Contents

1. [Overview](#overview)
2. [Service Management](#service-management) ⚠️ **Required Reading**
3. [Architecture](#architecture)
4. [Installation Scenarios](#installation-scenarios)
5. [Components](#components)
6. [Configuration](#configuration)
7. [Container Network Architecture](#container-network-architecture)
8. [Configuration Management](#configuration-management)
9. [Sample Configurations](#sample-configurations)

## Overview

Flight Control provides flexible observability solutions to meet different deployment scenarios. The system supports two main use cases:

### Use Case 1: External Observability Stack Integration

**Scenario**: You already have an existing observability stack (Prometheus, Grafana, Jaeger, etc.) and want to integrate Flight Control telemetry into it.

**Solution**: Deploy only the **Telemetry Gateway** as a bridge between Flight Control and your external observability infrastructure.

**Benefits**:

- Minimal resource footprint
- Integrates with existing monitoring workflows
- Centralized observability across multiple systems
- Flexible data routing and transformation

### Use Case 2: Standalone Observability Stack

**Scenario**: You need a complete, self-contained observability solution for Flight Control.

**Solution**: Deploy the **full observability stack** including:

- **Grafana** for visualization and dashboards
- **Prometheus** for metrics collection and storage (internal only)
- **Telemetry Gateway** for telemetry data processing and forwarding
- **UserInfo Proxy** for AAP OAuth integration (optional)

**Benefits**:

- Complete out-of-the-box monitoring solution
- Pre-configured Flight Control dashboards
- Integrated authentication with AAP
- No external dependencies

All components run as Podman containers managed by systemd, providing enterprise-grade reliability and integration with existing infrastructure.

**Important**: Both the standalone Telemetry Gateway and the full observability stack can be installed and operated independently without requiring core Flight Control services (`flightctl-api`, `flightctl-worker`, `flightctl-db`, `flightctl-kv`) to be running. This allows you to set up observability infrastructure before or alongside your main Flight Control deployment.

## Service Management

**🔑 Service management uses native systemd targets:**

```bash
# For Telemetry Gateway only (minimal setup)
sudo systemctl start flightctl-telemetry-gateway.target
sudo systemctl stop flightctl-telemetry-gateway.target

# For full observability stack (includes telemetry-gateway + Grafana + Prometheus)
sudo systemctl start flightctl-observability.target  
sudo systemctl stop flightctl-observability.target

# For automatic startup on boot
sudo systemctl enable flightctl-observability.target
```

**Configuration management is separate:**

```bash
# When you change /etc/flightctl/service-config.yaml
# First, ensure root is logged in to flightctl
sudo flightctl login

# Then regenerate configs
sudo flightctl-render-observability      # Regenerate configs
sudo systemctl restart flightctl-observability.target  # Apply changes
```

**Systemd targets provide:**

- ✅ Proper dependency management and startup order
- ✅ Network dependencies automatically handled  
- ✅ Standard systemd enable/disable functionality
- ✅ Integration with system boot process
- ✅ **Grouped start/stop**: Stopping target stops all related services

**❌ Do not use individual service commands:**

- `systemctl start flightctl-grafana.service` (use targets instead)
- `systemctl start flightctl-prometheus.service` (use targets instead)

**Two-step process:**

1. **Config changes**: `sudo flightctl login` then `sudo flightctl-render-observability` (renders templates)
2. **Service management**: `sudo systemctl start/stop/restart flightctl-observability.target`

## Architecture

### High-Level Architecture

```text
┌─────────────────────────────────────────────────────────────────┐
│                    Flight Control Observability Stack           │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌─────────────┐    ┌──────────────┐    ┌─────────────────────┐ │
│  │   Grafana   │    │  Prometheus  │    │ Telemetry Gateway   │ │
│  │ Dashboard   │◄───┤  Metrics     │◄───┤ (Internal)          │ │
│  │ (Port 3000) │    │  (Port 9090) │    │ (Port 4317/9464)    │ │
│  └─────────────┘    └──────────────┘    └─────────────────────┘ │
│         │                                                       │
│         ▼                                                       │
│  ┌─────────────┐                                                │
│  │ UserInfo    │                                                │
│  │ Proxy       │                                                │
│  │ (Internal)  │                                                │
│  └─────────────┘                                                │
│         │                                                       │
│         ▼                                                       │
│  ┌─────────────┐                                                │
│  │ Identity    │                                                │
│  │ Provider    │                                                │
│  │ (External)  │                                                │
│  └─────────────┘                                                │
└─────────────────────────────────────────────────────────────────┘
```

### Container Network Architecture

All observability components communicate within the `flightctl` Podman network:

```text
flightctl Network (Internal)
├── flightctl-grafana:3000
├── flightctl-prometheus:9090 (internal only)
├── flightctl-telemetry-gateway:4317 (gRPC), 9464 (Prometheus metrics)
└── flightctl-userinfo-proxy:8080 (internal only)

External Access (Published Ports)
├── <host>:3000 → flightctl-grafana:3000 (full stack only)
├── <host>:4317 → flightctl-telemetry-gateway:4317 (gRPC)
└── <host>:9464 → flightctl-telemetry-gateway:9464 (Prometheus metrics)
```

**Key Design Principles:**

- **Internal Communication**: Containers communicate via container names (e.g., `flightctl-prometheus:9090`)
- **External Access**: Telemetry Gateway always exposes external ports; Grafana only in full stack mode
- **Security**: Prometheus and UserInfo proxy are internal-only for security
- **Network Isolation**: All components isolated within the `flightctl` network
- **Flexibility**: Telemetry Gateway can forward data to external systems or internal Prometheus

**Network Configuration:**

The observability services use the shared `flightctl` Podman network that is managed by the core Flight Control services. This network:

- Provides integration with other Flight Control services
- Enables secure internal communication between observability components
- Allows containers to reference each other by name (e.g., `flightctl-prometheus:9090`)
- Is created and managed by the core `flightctl-services` package
- Is shared between all Flight Control components including observability services

## Installation Order

The observability services require the `flightctl` network from the core Flight Control services, but you have flexibility in installation order:

### Option 1: Services First (Recommended)

1. Install core Flight Control services (`flightctl-services`) to create the `flightctl` network
2. Install observability services (`flightctl-telemetry-gateway` or `flightctl-observability`)
3. Log in as root: `sudo flightctl login`
4. Run `sudo flightctl-render-observability` to generate configuration files
5. Start observability services with systemd targets
6. Core services will automatically send telemetry to existing observability infrastructure

### Option 2: Simultaneous Installation

1. Install both core services (`flightctl-services`) and observability services
2. Log in as root: `sudo flightctl login`
3. Run `sudo flightctl-render-observability` to generate configuration files
4. Start observability services with systemd targets
5. The `flightctl` network will be available for observability services

**Note**: The observability services will fail to start if the `flightctl` network is not available or if configuration files haven't been generated, so ensure the core services are installed first and configuration is rendered.

## Installation Scenarios

### Scenario 1: External Observability Stack Integration

**When to Use**: You already have an existing observability infrastructure (Prometheus, Grafana, Jaeger, etc.) and want to integrate Flight Control telemetry into it.

**Components Included**:

- Telemetry Gateway only (external ports 4317, 9464)

**Prerequisites**:

- Podman and systemd installed
- External observability stack configured to receive OTLP data

**Note**: Telemetry Gateway requires the `flightctl` network, which is provided by the `flightctl-services` package

**Installation**:

```bash
# Install the Telemetry Gateway (requires flightctl-services for the network)
sudo dnf install flightctl-telemetry-gateway

# The package automatically:
# 1. Checks prerequisites
# 2. Generates telemetry gateway configuration
# 3. Configures systemd service (but does not start or enable it)

# Generate configuration files from service-config.yaml:
# First, ensure root is logged in to flightctl
sudo flightctl login

# Then render configuration templates
sudo flightctl-render-observability

# To start the telemetry gateway service:
sudo systemctl start flightctl-telemetry-gateway.target

# For automatic startup on boot:
sudo systemctl enable flightctl-telemetry-gateway.target

**Note**: The installation process only configures the service but does not automatically start it. You must run `flightctl-render-observability` to generate configuration files before starting the service.
```

**Configuration**: Configure the telemetry gateway to forward data to your external systems:

```yaml
# /etc/flightctl/service-config.yaml
observability:
  telemetry_gateway:
    image: quay.io/flightctl/flightctl-telemetry-gateway:latest
    grpc_port: 4317
    prometheus_port: 9464
    config:
      telemetryGateway:
        export:
          prometheus: 0.0.0.0:9464
        forward:
          endpoint: "your-otel-collector.company.com:4317"
          tls:
            insecureSkipTlsVerify: false
```

### Scenario 2: Standalone Observability Stack

**When to Use**: You need a complete, self-contained observability solution for Flight Control without external dependencies.

**Components Included**:

- Grafana dashboard (external port 3000)
- Prometheus metrics (internal only - accessed via Grafana)
- Telemetry Gateway (external ports 4317, 9464)
- UserInfo proxy (internal only, optional for AAP integration)

**Prerequisites**:

- Podman and systemd installed

**Note**: Observability stack requires the `flightctl` network from the `flightctl-services` package

**Installation**:

```bash
# Install the full observability package (requires flightctl-services for the network)
sudo dnf install flightctl-observability

# The package automatically:
# 1. Checks prerequisites
# 2. Generates initial configuration
# 3. Configures systemd services (but does not start or enable them)

# Generate configuration files from service-config.yaml:
# First, ensure root is logged in to flightctl
sudo flightctl login

# Then render configuration templates
sudo flightctl-render-observability

# To start all observability services:
sudo systemctl start flightctl-observability.target

# For automatic startup on boot:
sudo systemctl enable flightctl-observability.target

**Note**: The installation process only configures the services but does not automatically start them. You must run `flightctl-render-observability` to generate configuration files before starting the services.
```

**Access**:

- Grafana dashboard: `http://<host>:3000` (default port, configurable)
- Prometheus metrics: Available through Grafana (internal only)
- Telemetry Gateway: `<host>:4317` (gRPC), `<host>:9464` (Prometheus metrics)

**Access Methods**:

- **Local deployment**: `http://localhost:3000`
- **Remote deployment**: `http://server-ip:3000` or `http://server-hostname:3000`
- **Custom domain**: `http://grafana.yourdomain.com:3000` (with proper DNS/proxy setup)
- **Custom port**: Configure `published_port` in service-config.yaml

## Components

### Grafana Dashboard

**Purpose**: Web-based visualization and dashboards for metrics and logs.

**Key Features**:

- Pre-configured Flight Control dashboards
- OAuth integration with identity providers
- HTTPS support with custom certificates
- Automatic Prometheus data source configuration

**Configuration**:

- **Internal Address**: `flightctl-grafana:3000`
- **External Access**: `http://<host>:3000` (configurable port)
- **Data Storage**: `/var/lib/grafana`

**Available Options in service-config.yaml**:

```yaml
observability:
  grafana:
    image: docker.io/grafana/grafana:latest
    published_port: 3000
    oauth:
      enabled: false
      client_id: your-oauth-client-id
      auth_url: https://your-aap.com/o/authorize
      token_url: https://your-aap.com/o/token/
      api_url: http://flightctl-userinfo-proxy:8080/userinfo
      tls_skip_verify: false
      local_admin_user: admin
      local_admin_password: secure-password
    protocol: http  # or https
    https:
      cert_file: /etc/grafana/certs/grafana.crt
      cert_key: /etc/grafana/certs/grafana.key
    allowed_unsigned_plugins: ""  # Optional: comma-separated list of plugin IDs to allow unsigned plugins
```

### Prometheus Metrics

**Purpose**: Time-series database for metrics collection and storage.

**Key Features**:

- Automatic Flight Control service discovery
- Configurable retention policies
- Built-in query interface
- Integration with Grafana

**Configuration**:

- **Internal Address**: `flightctl-prometheus:9090`
- **External Access**: None (internal only - accessed via Grafana)
- **Data Storage**: `/var/lib/prometheus`

**Available Options in service-config.yaml**:

```yaml
observability:
  prometheus:
    image: docker.io/prom/prometheus:latest
    # No published_port - internal only
```

**Note**: Prometheus configuration is automatically generated to scrape Flight Control services and the OpenTelemetry collector.

### Telemetry Gateway

**Purpose**: Flight Control-specific telemetry data collection, processing, and forwarding with device authentication and attribute enrichment.

**Key Features**:

- Device authentication and authorization
- Device attribute enrichment and processing
- OTLP protocol support for telemetry ingestion
- Flexible forwarding to external systems or internal Prometheus
- TLS-secured communication with device certificates
- Prometheus metrics export endpoint

**Configuration**:

- **Internal Address**: `flightctl-telemetry-gateway:4317` (gRPC), `flightctl-telemetry-gateway:9464` (Prometheus metrics)
- **External Access**: `<host>:4317` (gRPC), `<host>:9464` (Prometheus metrics) - configurable ports

**Available Options in service-config.yaml**:

```yaml
observability:
  telemetry_gateway:
    image: quay.io/flightctl/flightctl-telemetry-gateway:latest
    grpc_port: 4317  # External gRPC port
    prometheus_port: 9464  # External Prometheus metrics port
```

**Telemetry Gateway Configuration**:

The telemetry gateway configuration is nested under the observability section:

```yaml
observability:
  telemetry_gateway:
    image: quay.io/flightctl/flightctl-telemetry-gateway:latest
    grpc_port: 4317
    prometheus_port: 9464
    config:
      telemetryGateway:
        logLevel: "info"
        tls:
          certFile: "/etc/telemetry-gateway/certs/server.crt"
          keyFile: "/etc/telemetry-gateway/certs/server.key"
          caCert: "/etc/telemetry-gateway/certs/ca.crt"
        listen:
          device: "0.0.0.0:4317"
        export:
          prometheus: "0.0.0.0:9464"  # Internal Prometheus
        forward:
          endpoint: "external-collector.company.com:4317"
          tls:
            insecureSkipTlsVerify: false
            certFile: "/etc/telemetry-gateway/certs/client.crt"
            keyFile: "/etc/telemetry-gateway/certs/client.key"
            caFile: "/etc/telemetry-gateway/certs/ca.crt"
```

**Note**: The `config` field contains the telemetry gateway configuration as a YAML object. The `flightctl-render-observability` script extracts this configuration using Python and `PyYAML`, and writes it to `/etc/flightctl/flightctl-telemetry-gateway/config.yaml`.

### UserInfo Proxy

**Purpose**: OAuth UserInfo endpoint proxy specifically designed for Ansible Automation Platform (AAP) integration with Grafana.

**Key Features**:

- AAP-specific OAuth UserInfo endpoint translation
- Configurable TLS verification
- AAP user structure to OpenID Connect UserInfo transformation
- Grafana role mapping based on AAP permissions (is_superuser, is_platform_auditor)

**Configuration**:

- **Internal Address**: `flightctl-userinfo-proxy:8080`
- **External Access**: None (internal only)

**Available Options in service-config.yaml**:

```yaml
observability:
  userinfo_proxy:
    image: flightctl/userinfo-proxy:latest
    upstream_url: https://your-aap-instance.com/api/gateway/v1/me/
    skip_tls_verify: false  # Set to true for self-signed certificates
```

**Note**: UserInfo proxy is specifically designed for AAP (Ansible Automation Platform) integration and transforms AAP user API responses to OpenID Connect UserInfo format.

## Configuration

All observability configuration is centralized in `/etc/flightctl/service-config.yaml` under the `observability` section.

### Complete Configuration Reference

```yaml
observability:
  # Grafana Configuration
  grafana:
    image: docker.io/grafana/grafana:latest
    published_port: 3000  # External port - can be changed (e.g., 8080, 3001, etc.)
    
    # OAuth Integration
    oauth:
      enabled: true
      client_id: your-oauth-client-id
      auth_url: https://your-idp.com/o/authorize
      token_url: https://your-idp.com/o/token/
      api_url: http://flightctl-userinfo-proxy:8080/userinfo  # Internal container communication
      scopes: read  # OAuth scopes to request
      tls_skip_verify: false  # Skip TLS verification for OAuth endpoints
    
    # Local Admin Configuration (used when OAuth is disabled)
    local_admin_user: admin
    local_admin_password: secure-password
    
    # Server Configuration
    root_url: http://localhost:3000  # Root URL for Grafana (used for OAuth redirects)
    protocol: http  # or https
    
    # HTTPS Configuration (Optional - only needed when protocol: https)
    https:
      cert_file: /etc/grafana/certs/grafana.crt
      cert_key: /etc/grafana/certs/grafana.key
    
    # Plugin Configuration (Optional)
    allowed_unsigned_plugins: ""  # Comma-separated list of plugin IDs to allow unsigned plugins

  # Prometheus Configuration (internal only)
  prometheus:
    image: docker.io/prom/prometheus:latest

  # OpenTelemetry Collector Configuration
  otel_collector:
    image: docker.io/otel/opentelemetry-collector-contrib:latest
    grpc_port: 4317  # External gRPC port - configurable
    http_port: 4318  # External HTTP port - configurable

  # UserInfo Proxy Configuration (Optional - AAP specific)
  userinfo_proxy:
    image: flightctl/userinfo-proxy:latest
    upstream_url: https://your-aap-instance.com/api/gateway/v1/me/
    skip_tls_verify: false  # Skip TLS verification for upstream calls
```

### Key Configuration Principles

1. **All configuration is in service-config.yaml**: No need to edit individual container files or environment variables
2. **Automatic template generation**: The system automatically generates container configurations from your service-config.yaml
3. **Hot configuration**: Use `sudo flightctl login` then `sudo flightctl-render-observability` then restart services with systemd targets
4. **Built-in validation**: The system automatically validates your configuration and provides clear error messages

## Configuration Management

### Configuration Management System

Flight Control separates configuration management from service management for better control.

#### Configuration Rendering

**Render Configuration Templates**:

```bash
# First, ensure root is logged in to flightctl
sudo flightctl login

# Then render configuration templates
sudo flightctl-render-observability
```

**What it does**:

1. Validates prerequisites and configuration
2. Automatically validates YAML syntax and configuration structure  
3. Renders configuration templates from `/etc/flightctl/service-config.yaml`
4. Reloads systemd daemon
5. **Does NOT start or stop services**

#### Service Management

**Start/Stop Services**:

```bash
# Start services
sudo systemctl start flightctl-observability.target       # Full stack
sudo systemctl start flightctl-telemetry-gateway.target   # Telemetry Gateway only

# Stop services  
sudo systemctl stop flightctl-observability.target        # Full stack
sudo systemctl stop flightctl-telemetry-gateway.target    # Telemetry Gateway only

# Restart services (after config changes)
sudo systemctl restart flightctl-observability.target     # Full stack
sudo systemctl restart flightctl-telemetry-gateway.target # Telemetry Gateway only

# Enable automatic startup
sudo systemctl enable flightctl-observability.target      # Full stack
sudo systemctl enable flightctl-telemetry-gateway.target  # Telemetry Gateway only
```

These commands work whether you have the full observability stack, standalone Telemetry Gateway, or any combination of components.

### Configuration Workflow

1. **Edit configuration**:

   ```bash
   sudo vim /etc/flightctl/service-config.yaml
   ```

2. **Render updated configuration**:

   ```bash
   # Ensure root is logged in to flightctl
   sudo flightctl login
   
   # Render configuration templates
   sudo flightctl-render-observability
   ```

3. **Apply changes to running services**:

   ```bash
   sudo systemctl restart flightctl-observability.target
   ```

**Stop services (when needed)**:

```bash
sudo systemctl stop flightctl-observability.target
```

Use this command when you need to stop all observability services for maintenance or troubleshooting.

## UserInfo Proxy Setup (AAP Integration)

The UserInfo proxy enables Grafana OAuth integration specifically with Ansible Automation Platform (AAP) by translating AAP's user API responses to the standard OpenID Connect UserInfo format that Grafana expects.

### Purpose and Benefits

**Why Use UserInfo Proxy with AAP?**

- **AAP-Specific Translation**: Converts AAP's user API responses to standard OAuth UserInfo format
- **Permission Mapping**: Maps AAP user permissions (is_superuser, is_platform_auditor) to Grafana roles
- **TLS Management**: Centralized TLS verification settings for AAP connections
- **Grafana Integration**: Optimized for Grafana's OAuth requirements with AAP
- **Security**: Internal-only service with no external exposure

### Configuration

The UserInfo proxy runs as an internal service and requires minimal configuration:

```yaml
observability:
  grafana:
    oauth:
      enabled: true
      client_id: your-oauth-client-id
      auth_url: https://your-idp.com/o/authorize
      token_url: https://your-idp.com/o/token/
      api_url: http://flightctl-userinfo-proxy:8080/userinfo  # Points to internal proxy
      
  userinfo_proxy:
    upstream_url: https://your-aap-instance.com/api/gateway/v1/me/  # Your AAP instance's user API endpoint
    skip_tls_verify: false  # Set to true for self-signed certificates
```

### Communication Flow

```text
User Authentication Flow with AAP:
1. User → Grafana → AAP (OAuth login)
2. Grafana receives OAuth token from AAP
3. Grafana calls api_url → UserInfo Proxy (internal)
4. UserInfo Proxy → AAP User API (with token)
5. UserInfo Proxy transforms AAP response → Grafana
6. Grafana creates user session with mapped roles
```

### Response Transformation

The proxy transforms AAP API responses to standard UserInfo format:

**Input (AAP API Response)**:

```json
{
  "count": 1,
  "results": [{
    "id": 123,
    "username": "john.doe",
    "email": "john.doe@company.com",
    "first_name": "John",
    "last_name": "Doe",
    "is_superuser": true,
    "is_platform_auditor": false
  }]
}
```

**Output (UserInfo Standard)**:

```json
{
  "sub": "123",
  "preferred_username": "john.doe",
  "email": "john.doe@company.com",
  "email_verified": true,
  "name": "John Doe",
  "given_name": "John",
  "family_name": "Doe",
  "roles": ["Admin"],
  "groups": ["admin"],
  "updated_at": 1640995200
}
```

### TLS Configuration

The proxy supports flexible TLS verification:

```yaml
userinfo_proxy:
  upstream_url: https://your-aap-instance.com/api/gateway/v1/me/
  skip_tls_verify: false  # Default: strict TLS verification
```

**When to use `skip_tls_verify: true`**:

- Development environments
- Self-signed certificates
- Internal PKI that's not in system trust store
- Testing scenarios

**Production recommendation**: Always use `skip_tls_verify: false` with proper certificates.

## Sample Configurations

### External Observability Integration

Minimal configuration for integrating with existing observability stack:

```yaml
observability:
  telemetry_gateway:
    image: quay.io/flightctl/flightctl-telemetry-gateway:latest
    grpc_port: 4317
    prometheus_port: 9464
    config:
      telemetryGateway:
        forward:
          endpoint: "your-external-collector.company.com:4317"
          tls:
            insecureSkipTlsVerify: false
        export:
          prometheus: 0.0.0.0:9464
```

**Note**: The telemetry gateway configuration is provided as a YAML object in the `config` field. The `flightctl-render-observability` script extracts this configuration using Python and `PyYAML`, and writes it to `/etc/flightctl/flightctl-telemetry-gateway/config.yaml`.

**Management Commands Available**:

Even when installing only the Telemetry Gateway, you have access to the management commands:

- `sudo flightctl login` - Log in as root to flightctl (required before rendering)
- `sudo flightctl-render-observability` - Render configuration templates from `service-config.yaml`
- `sudo systemctl start/stop/restart flightctl-telemetry-gateway.target` - Manage telemetry gateway service

**Two-step process**: First log in with `sudo flightctl login`, then render configuration with `sudo flightctl-render-observability`, then manage services with systemd targets. This separation provides better control and follows systemd best practices.

### Standalone Stack Configuration (No OAuth)

Complete standalone stack with local authentication only:

```yaml
observability:
  grafana:
    image: docker.io/grafana/grafana:latest
    published_port: 3000
    oauth:
      enabled: false
    
    local_admin_user: admin
    local_admin_password: secure-password

  prometheus:
    image: docker.io/prom/prometheus:latest

  telemetry_gateway:
    image: quay.io/flightctl/flightctl-telemetry-gateway:latest
    grpc_port: 4317
    prometheus_port: 9464
    config:
      telemetryGateway:
        export:
          prometheus: "0.0.0.0:9464"
```

### OAuth Integration with AAP

Complete OAuth setup with Ansible Automation Platform:

```yaml
observability:
  grafana:
    image: docker.io/grafana/grafana:latest
    published_port: 3000
    oauth:
      enabled: true
      client_id: flightctl-grafana-client
      auth_url: https://your-aap-instance.com/o/authorize
      token_url: https://your-aap-instance.com/o/token/
      api_url: http://flightctl-userinfo-proxy:8080/userinfo
      scopes: read
      tls_skip_verify: false
    
    local_admin_user: admin
    local_admin_password: fallback-password

  prometheus:
    image: docker.io/prom/prometheus:latest

  telemetry_gateway:
    image: quay.io/flightctl/flightctl-telemetry-gateway:latest
    grpc_port: 4317
    prometheus_port: 9464
    config:
      telemetryGateway:
        export:
          prometheus: "0.0.0.0:9464"

  userinfo_proxy:
    image: flightctl/userinfo-proxy:latest
    upstream_url: https://your-aap-instance.com/api/gateway/v1/me/
    skip_tls_verify: false
```

### Secure Grafana with Custom TLS Certificates

Secure Grafana setup with custom TLS certificates:

```yaml
observability:
  grafana:
    image: docker.io/grafana/grafana:latest
    published_port: 3443
    protocol: https
    https:
      cert_file: /etc/grafana/certs/grafana.crt
      cert_key: /etc/grafana/certs/grafana.key
    oauth:
      enabled: true
      client_id: flightctl-grafana-client
      auth_url: https://your-aap-instance.com/o/authorize
      token_url: https://your-aap-instance.com/o/token/
      api_url: http://flightctl-userinfo-proxy:8080/userinfo
      scopes: read
      tls_skip_verify: false

  prometheus:
    image: docker.io/prom/prometheus:latest

  telemetry_gateway:
    image: quay.io/flightctl/flightctl-telemetry-gateway:latest
    grpc_port: 4317
    prometheus_port: 9464
    config:
      telemetryGateway:
        export:
          prometheus: "0.0.0.0:9464"

  userinfo_proxy:
    image: flightctl/userinfo-proxy:latest
    upstream_url: https://your-aap-instance.com/api/gateway/v1/me/
    skip_tls_verify: false
```

**Certificate Setup**:

```bash
# Place certificates in the expected location
sudo mkdir -p /etc/grafana/certs
sudo cp your-grafana.crt /etc/grafana/certs/grafana.crt
sudo cp your-grafana.key /etc/grafana/certs/grafana.key
sudo chown 472:472 /etc/grafana/certs/grafana.*
sudo chmod 600 /etc/grafana/certs/grafana.key
```

### Development Environment with Self-Signed Certificates

Development setup with relaxed TLS verification:

```yaml
observability:
  grafana:
    image: docker.io/grafana/grafana:latest
    published_port: 3000
    oauth:
      enabled: true
      client_id: dev-grafana-client
      auth_url: https://dev-auth.local/o/authorize
      token_url: https://dev-auth.local/o/token/
      api_url: http://flightctl-userinfo-proxy:8080/userinfo
      scopes: read
      tls_skip_verify: true  # OK for development
    
    local_admin_user: admin
    local_admin_password: dev-password

  prometheus:
    image: docker.io/prom/prometheus:latest

  telemetry_gateway:
    image: quay.io/flightctl/flightctl-telemetry-gateway:latest
    grpc_port: 4317
    prometheus_port: 9464
    config:
      telemetryGateway:
        export:
          prometheus: "0.0.0.0:9464"

  userinfo_proxy:
    image: flightctl/userinfo-proxy:latest
    upstream_url: https://dev-aap-instance.local/api/gateway/v1/me/
    skip_tls_verify: true  # OK for development with self-signed certs
```

### External Integration Only

For integrating with existing observability infrastructure:

```yaml
observability:
  telemetry_gateway:
    image: quay.io/flightctl/flightctl-telemetry-gateway:latest
    grpc_port: 4317
    prometheus_port: 9464
    config:
      telemetryGateway:
        forward:
          endpoint: "your-external-collector.company.com:4317"
          tls:
            insecureSkipTlsVerify: false
        export:
          prometheus: 0.0.0.0:9464
```

**Note**: The telemetry gateway automatically configures itself based on the `observability.telemetry_gateway.config.telemetryGateway` section in `service-config.yaml`. No manual OpenTelemetry configuration files are needed.

### Custom Port Configuration

Example with custom ports to avoid conflicts:

```yaml
observability:
  grafana:
    image: docker.io/grafana/grafana:latest
    published_port: 8080  # Use port 8080 instead of 3000
    oauth:
      enabled: false
    
    local_admin_user: admin
    local_admin_password: secure-password

  prometheus:
    image: docker.io/prom/prometheus:latest

  telemetry_gateway:
    image: quay.io/flightctl/flightctl-telemetry-gateway:latest
    grpc_port: 14317  # Use port 14317 instead of 4317
    prometheus_port: 19464  # Use port 19464 instead of 9464
    config:
      telemetryGateway:
        export:
          prometheus: "0.0.0.0:9464"
```

**Access with custom ports**:

- Grafana: `http://<host>:8080`
- Telemetry Gateway: `<host>:14317` (gRPC), `<host>:19464` (Prometheus metrics)

## Configuration Reference

This section provides detailed documentation for every configuration variable available in the Flight Control observability stack.

### Grafana Configuration Variables

#### Container Configuration

**`observability.grafana.image`**

- **Type**: String
- **Default**: `docker.io/grafana/grafana:latest`
- **Description**: Container image for Grafana. Can specify a specific version tag for reproducible deployments.
- **Example**: `docker.io/grafana/grafana:10.2.0`

**`observability.grafana.published_port`**

- **Type**: Integer
- **Default**: `3000`
- **Description**: External port where Grafana web interface will be accessible. Change this if port 3000 conflicts with other services.
- **Example**: `8080`

#### Protocol Configuration

**`observability.grafana.protocol`**

- **Type**: String
- **Default**: `http`
- **Valid Values**: `http`, `https`
- **Description**: Protocol for Grafana web interface. Set to `https` to enable TLS encryption.
- **Example**: `https`

**`observability.grafana.https.cert_file`**

- **Type**: String
- **Default**: `/etc/grafana/certs/grafana.crt`
- **Description**: Path to TLS certificate file. Only used when `protocol: https`. Certificate must be readable by Grafana container (UID 472).
- **Required**: When `protocol: https`
- **Example**: `/etc/grafana/certs/my-grafana.crt`

**`observability.grafana.https.cert_key`**

- **Type**: String
- **Default**: `/etc/grafana/certs/grafana.key`
- **Description**: Path to TLS private key file. Only used when `protocol: https`. Key must be readable by Grafana container (UID 472) and have restricted permissions (600).
- **Required**: When `protocol: https`
- **Example**: `/etc/grafana/certs/my-grafana.key`

#### OAuth Configuration

**`observability.grafana.oauth.enabled`**

- **Type**: Boolean
- **Default**: `false`
- **Description**: Enable OAuth authentication. When enabled, users will authenticate through external identity provider instead of local Grafana accounts.
- **Example**: `true`

**`observability.grafana.oauth.client_id`**

- **Type**: String
- **Default**: Empty
- **Description**: OAuth client ID registered with your identity provider. Must match the client ID configured in your IdP.
- **Required**: When `oauth.enabled: true`
- **Example**: `flightctl-grafana-client`

**`observability.grafana.oauth.auth_url`**

- **Type**: String (URL)
- **Default**: Empty
- **Description**: OAuth authorization endpoint URL. Users will be redirected here to authenticate.
- **Required**: When `oauth.enabled: true`
- **Example**: `https://your-aap.com/o/authorize`

**`observability.grafana.oauth.token_url`**

- **Type**: String (URL)
- **Default**: Empty
- **Description**: OAuth token endpoint URL. Grafana exchanges authorization codes for access tokens here. Must end with a '/' character.
- **Required**: When `oauth.enabled: true`
- **Example**: `https://your-aap.com/o/token/`

**`observability.grafana.oauth.api_url`**

- **Type**: String (URL)
- **Default**: Empty
- **Description**: OAuth user info API endpoint. Grafana calls this to get user information. For AAP integration, use the UserInfo proxy: `http://flightctl-userinfo-proxy:8080/userinfo`
- **Required**: When `oauth.enabled: true`
- **Example**: `http://flightctl-userinfo-proxy:8080/userinfo`

**`observability.grafana.oauth.scopes`**

- **Type**: String
- **Default**: `read`
- **Description**: OAuth scopes to request from the identity provider. Specifies what permissions the OAuth application should request.
- **Example**: `read`, `read write`, `openid profile email`

**`observability.grafana.oauth.tls_skip_verify`**

- **Type**: Boolean
- **Default**: `false`
- **Description**: Skip TLS certificate verification when connecting to OAuth endpoints. Only use `true` in development environments with self-signed certificates.
- **Security**: Always use `false` in production
- **Example**: `true` (development only)

#### Local Admin Configuration

**`observability.grafana.local_admin_user`**

- **Type**: String
- **Default**: `admin`
- **Description**: Username for local Grafana admin account. This account can be used as fallback when OAuth is unavailable or disabled.
- **Example**: `admin`

**`observability.grafana.local_admin_password`**

- **Type**: String
- **Default**: `defaultadmin`
- **Description**: Password for local Grafana admin account. Change this from the default for security.
- **Security**: Use a strong password in production
- **Example**: `secure-admin-password-123`

#### Server Configuration

**`observability.grafana.root_url`**

- **Type**: String (URL)
- **Default**: `http://localhost:3000`
- **Description**: Root URL for Grafana. Used for OAuth redirects and asset loading. Should match the external URL where Grafana is accessible.
- **Example**: `https://grafana.yourdomain.com`, `http://server-ip:3000`

#### Plugin Configuration

**`observability.grafana.allowed_unsigned_plugins`**

- **Type**: String (comma-separated list)
- **Default**: Empty (no unsigned plugins allowed)
- **Description**: Comma-separated list of plugin IDs that are allowed to load even if they are not signed by Grafana Labs. This is useful for custom plugins or community plugins that haven't been signed. Use with caution as unsigned plugins can pose security risks.
- **Security**: Only specify plugins you trust. Use specific plugin IDs rather than wildcards in production environments.
- **Example**: `my-custom-plugin,another-plugin`
- **Warning**: Setting this to `*` allows all unsigned plugins, which is not recommended for security reasons.

### Prometheus Configuration Variables

**`observability.prometheus.image`**

- **Type**: String
- **Default**: `docker.io/prom/prometheus:latest`
- **Description**: Container image for Prometheus. Prometheus runs as internal-only service with no external ports.
- **Example**: `docker.io/prom/prometheus:v2.45.0`

### Telemetry Gateway Configuration Variables

**`observability.telemetry_gateway.image`**

- **Type**: String
- **Default**: `quay.io/flightctl/flightctl-telemetry-gateway:latest`
- **Description**: Container image for Flight Control Telemetry Gateway. This is a specialized OpenTelemetry collector with device authentication and attribute enrichment.
- **Example**: `quay.io/flightctl/flightctl-telemetry-gateway:v1.0.0`

**`observability.telemetry_gateway.grpc_port`**

- **Type**: Integer
- **Default**: `4317`
- **Description**: External port for OpenTelemetry gRPC receiver. Flight Control agents send telemetry data to this port using OTLP/gRPC protocol.
- **Example**: `14317`

**`observability.telemetry_gateway.prometheus_port`**

- **Type**: Integer
- **Default**: `9464`
- **Description**: External port for Prometheus metrics endpoint. This exposes the telemetry gateway's own metrics for monitoring.
- **Example**: `19464`

### Telemetry Gateway Configuration Variables

The telemetry gateway uses a nested YAML object in the `config` field for its internal OpenTelemetry collector configuration:

**`observability.telemetry_gateway.config`**

- **Type**: YAML Object
- **Default**: Empty
- **Description**: Telemetry gateway configuration as a YAML object. The `flightctl-render-observability` script extracts this configuration using Python and `PyYAML`, and writes it to `/etc/flightctl/flightctl-telemetry-gateway/config.yaml`.
- **Example**: See the sample configurations above for complete examples.

**Configuration Structure**:

The `config` field contains a YAML object with the following structure:

```yaml
telemetryGateway:
  logLevel: "info"                    # Log level: debug, info, warn, error
  tls:
    certFile: "/etc/telemetry-gateway/certs/server.crt"
    keyFile: "/etc/telemetry-gateway/certs/server.key"
    caCert: "/etc/telemetry-gateway/certs/ca.crt"
  listen:
    device: "0.0.0.0:4317"           # Address and port for device connections
  export:
    prometheus: "0.0.0.0:9464"  # Prometheus export endpoint
  forward:
    endpoint: "external-collector.company.com:4317"  # External forwarding endpoint
    tls:
      insecureSkipTlsVerify: false    # TLS verification setting
      certFile: "/etc/telemetry-gateway/certs/client.crt"
      keyFile: "/etc/telemetry-gateway/certs/client.key"
      caFile: "/etc/telemetry-gateway/certs/ca.crt"
```

**Key Configuration Options**:

- **`logLevel`**: Controls telemetry gateway logging verbosity
- **`tls.*`**: Server-side TLS configuration for device connections
- **`listen.device`**: Address and port for receiving telemetry from devices
- **`export.prometheus`**: Endpoint for exporting metrics to Prometheus
- **`forward.*`**: Configuration for forwarding telemetry to external systems
- **`forward.tls.*`**: Client-side TLS configuration for external forwarding

#### Forward TLS Configuration Variables

These variables configure TLS client certificates for mTLS (mutual TLS) authentication when forwarding telemetry data to external OTLP collectors.

**`observability.telemetry_gateway.config.telemetryGateway.forward.tls.caFile`**

- **Type**: String (file path)
- **Default**: Empty
- **Description**: Path to the CA certificate file used to verify the external OTLP collector's server certificate. This certificate must be trusted to establish a secure connection to the external collector. The file path must be accessible from within the telemetry gateway container.
- **Required**: When using TLS with external forwarding (unless `insecureSkipTlsVerify` is set to `true`)
- **Example**: `/etc/telemetry-gateway/certs/forward-ca.crt`
- **Security**: Use a trusted CA certificate to verify the external collector's identity

**`observability.telemetry_gateway.config.telemetryGateway.forward.tls.certFile`**

- **Type**: String (file path)
- **Default**: Empty
- **Description**: Path to the client certificate file used for mTLS authentication when connecting to the external OTLP collector. This certificate is presented to the external collector to authenticate the telemetry gateway. The certificate must be signed by a CA trusted by the external collector. The file path must be accessible from within the telemetry gateway container.
- **Required**: When using mTLS authentication with external forwarding
- **Example**: `/etc/telemetry-gateway/certs/forward-client.crt`
- **Security**: Use a properly signed client certificate for secure authentication

**`observability.telemetry_gateway.config.telemetryGateway.forward.tls.keyFile`**

- **Type**: String (file path)
- **Default**: Empty
- **Description**: Path to the client private key file corresponding to the client certificate used for mTLS authentication. This key must match the certificate specified in `certFile`. The file path must be accessible from within the telemetry gateway container and must have restricted permissions (typically 600).
- **Required**: When using mTLS authentication with external forwarding (must be specified together with `certFile`)
- **Example**: `/etc/telemetry-gateway/certs/forward-client.key`
- **Security**: Ensure the private key file has restricted permissions and is kept secure

**TLS Forward Configuration Notes**:

- **File Paths**: All TLS certificate and key file paths must be accessible from within the telemetry gateway container. The files are mounted as volumes when specified in the configuration.
- **mTLS Setup**: For mutual TLS authentication, you must provide both `certFile` and `keyFile` (and optionally `caFile` for server verification).
- **Certificate Requirements**:
  - The client certificate must be signed by a CA trusted by the external collector
  - The CA certificate (`caFile`) must be trusted to verify the external collector's server certificate
  - Certificates must be in PEM format
- **Security Best Practices**:
  - Use proper certificate management and rotation
  - Restrict file permissions on private keys (600)
  - Use trusted CAs for production environments
  - Avoid using `insecureSkipTlsVerify: true` in production

### UserInfo Proxy Configuration Variables

**`observability.userinfo_proxy.image`**

- **Type**: String
- **Default**: `flightctl/userinfo-proxy:latest`
- **Description**: Container image for UserInfo proxy service. This service translates AAP user API responses to standard OAuth UserInfo format.
- **Example**: `flightctl/userinfo-proxy:v1.0.0`

**`observability.userinfo_proxy.upstream_url`**

- **Type**: String (URL)
- **Default**: Empty
- **Description**: URL of upstream identity provider's user API endpoint. For AAP integration, this should point to the AAP user API endpoint.
- **Required**: When using OAuth with AAP integration
- **Example**: `https://your-aap.com/api/gateway/v1/me/`

**`observability.userinfo_proxy.skip_tls_verify`**

- **Type**: Boolean
- **Default**: `false`
- **Description**: Skip TLS certificate verification when connecting to upstream user API. Only use `true` in development environments with self-signed certificates.
- **Security**: Always use `false` in production
- **Example**: `true` (development only)

### Configuration Validation Rules

#### Required Field Dependencies

When certain features are enabled, additional fields become required:

**OAuth Authentication** (`oauth.enabled: true`):

- `oauth.client_id` - Must be configured in your IdP
- `oauth.auth_url` - IdP authorization endpoint
- `oauth.token_url` - IdP token endpoint  
- `oauth.api_url` - User info endpoint (use UserInfo proxy for AAP)

**HTTPS Protocol** (`protocol: https`):

- `https.cert_file` - TLS certificate path
- `https.cert_key` - TLS private key path

**AAP Integration** (OAuth with UserInfo proxy):

- `userinfo_proxy.upstream_url` - AAP user API endpoint

**Telemetry Gateway Configuration**:

- The `observability.telemetry_gateway.config` field must contain a valid YAML object
- At least one of `config.telemetryGateway.export.prometheus` or `config.telemetryGateway.forward.endpoint` must be configured
- If `config.telemetryGateway.forward.tls` is configured, at least one TLS option must be specified

#### Default Behavior

- All fields are optional unless explicitly marked as required
- System uses documented default values for unspecified fields
- Empty sections (e.g., `prometheus: {}`) use all defaults
- Services are only created if their configuration sections exist

#### Security Guidelines

**Production Recommendations**:

- Change `local_admin_password` from default
- Use `tls_skip_verify: false` and `skip_tls_verify: false`
- Use `protocol: https` with valid certificates
- Use specific image tags instead of `latest`
- Use non-default ports if needed for security
- Configure proper TLS certificates for telemetry gateway
- Use `insecureSkipTlsVerify: false` in the telemetry gateway forward TLS configuration for production

**Development Allowances**:

- `tls_skip_verify: true` for self-signed certificates
- `skip_tls_verify: true` for internal development IdPs
- `insecureSkipTlsVerify: true` in the telemetry gateway forward TLS configuration for development
- Default passwords acceptable for local development
