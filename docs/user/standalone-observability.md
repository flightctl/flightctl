# FlightCtl Standalone Observability Stack

This comprehensive guide covers the complete FlightCtl observability stack, including installation, configuration, management, and troubleshooting.

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

FlightCtl provides flexible observability solutions to meet different deployment scenarios. The system supports two main use cases:

### Use Case 1: External Observability Stack Integration

**Scenario**: You already have an existing observability stack (Prometheus, Grafana, Jaeger, etc.) and want to integrate FlightCtl telemetry into it.

**Solution**: Deploy only the **OpenTelemetry Collector** as a bridge between FlightCtl and your external observability infrastructure.

**Benefits**:

- Minimal resource footprint
- Integrates with existing monitoring workflows
- Centralized observability across multiple systems
- Flexible data routing and transformation

### Use Case 2: Standalone Observability Stack

**Scenario**: You need a complete, self-contained observability solution for FlightCtl.

**Solution**: Deploy the **full observability stack** including:

- **Grafana** for visualization and dashboards
- **Prometheus** for metrics collection and storage (internal only)
- **OpenTelemetry Collector** for telemetry data processing
- **UserInfo Proxy** for AAP OAuth integration (optional)

**Benefits**:

- Complete out-of-the-box monitoring solution
- Pre-configured FlightCtl dashboards
- Integrated authentication with AAP
- No external dependencies

All components run as Podman containers managed by systemd, providing enterprise-grade reliability and integration with existing infrastructure.

**Important**: Both the standalone OpenTelemetry collector and the full observability stack can be installed and operated independently without requiring core FlightCtl services (flightctl-api, flightctl-worker, flightctl-db, flightctl-kv) to be running. This allows you to set up observability infrastructure before or alongside your main FlightCtl deployment.

## Service Management

**🔑 Service management uses native systemd targets:**

```bash
# For OpenTelemetry collector only (minimal setup)
sudo systemctl start flightctl-otel-collector.target
sudo systemctl stop flightctl-otel-collector.target

# For full observability stack (includes collector + Grafana + Prometheus)
sudo systemctl start flightctl-observability.target  
sudo systemctl stop flightctl-observability.target

# For automatic startup on boot
sudo systemctl enable flightctl-observability.target
```

**Configuration management is separate:**

```bash
# When you change /etc/flightctl/service-config.yaml
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

1. **Config changes**: `sudo flightctl-render-observability` (renders templates)
2. **Service management**: `sudo systemctl start/stop/restart flightctl-observability.target`

## Architecture

### High-Level Architecture

```text
┌─────────────────────────────────────────────────────────────────┐
│                    FlightCtl Observability Stack                │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌─────────────┐    ┌──────────────┐    ┌─────────────────────┐ │
│  │   Grafana   │    │  Prometheus  │    │ OpenTelemetry       │ │
│  │ Dashboard   │◄───┤  Metrics     │◄───┤ Collector           │ │
│  │ (Port 3000) │    │  (Port 9090) │    │ (Internal)          │ │
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

All observability components communicate within the `flightctl-observability` Podman network:

```text
flightctl-observability Network (Internal)
├── flightctl-grafana:3000
├── flightctl-prometheus:9090 (internal only)
├── flightctl-otel-collector:4317 (gRPC), 4318 (HTTP)
└── flightctl-userinfo-proxy:8080 (internal only)

External Access (Published Ports)
├── <host>:3000 → flightctl-grafana:3000 (full stack only)
├── <host>:4317 → flightctl-otel-collector:4317 (gRPC)
└── <host>:4318 → flightctl-otel-collector:4318 (HTTP)
```

**Key Design Principles:**

- **Internal Communication**: Containers communicate via container names (e.g., `flightctl-prometheus:9090`)
- **External Access**: Only OpenTelemetry collector always exposes external ports; Grafana only in full stack mode
- **Security**: Prometheus and UserInfo proxy are internal-only for security
- **Network Isolation**: All components isolated within the flightctl-observability network
- **Flexibility**: OpenTelemetry collector can forward data to external systems or internal Prometheus

**Network Configuration:**

The observability services use a dedicated Podman network named `flightctl-observability` that is automatically created and managed by the system. This network:

- Provides isolation from other FlightCtl services
- Enables secure internal communication between observability components
- Allows containers to reference each other by name (e.g., `flightctl-prometheus:9090`)
- Is automatically created when the first observability service starts
- Is shared between all observability components whether you install standalone OpenTelemetry collector or the full stack

## Installation Order

With the removal of dependencies on core FlightCtl services, you now have full flexibility in installation order:

### Option 1: Observability First

1. Install observability services (`flightctl-otel-collector` or `flightctl-observability`)
2. Configure and start observability services
3. Later install and configure core FlightCtl services
4. Core services will automatically send telemetry to existing observability infrastructure

### Option 2: Core Services First

1. Install and configure core FlightCtl services
2. Install observability services
3. Observability services will automatically collect telemetry from running core services

### Option 3: Simultaneous Installation

1. Install both core services and observability services
2. Configure and start all services in any order

This flexibility allows you to set up monitoring infrastructure independently of your main FlightCtl deployment timeline.

## Installation Scenarios

### Scenario 1: External Observability Stack Integration

**When to Use**: You already have an existing observability infrastructure (Prometheus, Grafana, Jaeger, etc.) and want to integrate FlightCtl telemetry into it.

**Components Included**:

- OpenTelemetry collector only (external ports 4317, 4318)

**Prerequisites**:

- Podman and systemd installed
- External observability stack configured to receive OTLP data

**Note**: OpenTelemetry collector can be installed and run independently of core FlightCtl services

**Installation**:

```bash
# Install only the OpenTelemetry collector
sudo dnf install flightctl-otel-collector

# The package automatically:
# 1. Checks prerequisites
# 2. Generates collector configuration
# 3. Configures systemd service (but does not start or enable it)

# To start the collector service:
sudo systemctl start flightctl-otel-collector.target

# For automatic startup on boot:
sudo systemctl enable flightctl-otel-collector.target

**Note**: The installation process only configures the service but does not automatically start it. Use systemd targets to start the observability stack.
```

**Configuration**: Configure the collector to forward data to your external systems:

```yaml
# /etc/otelcol/otelcol-config.yaml
exporters:
  prometheus:
    endpoint: "your-prometheus.company.com:9090"
  jaeger:
    endpoint: "your-jaeger.company.com:14250"
```

### Scenario 2: Standalone Observability Stack

**When to Use**: You need a complete, self-contained observability solution for FlightCtl without external dependencies.

**Components Included**:

- Grafana dashboard (external port 3000)
- Prometheus metrics (internal only - accessed via Grafana)
- OpenTelemetry collector (external ports 4317, 4318)
- UserInfo proxy (internal only, optional for AAP integration)

**Prerequisites**:

- Podman and systemd installed

**Note**: Observability stack can be installed and run independently of core FlightCtl services

**Installation**:

```bash
# Install the full observability package
sudo dnf install flightctl-observability

# The package automatically:
# 1. Checks prerequisites
# 2. Generates initial configuration
# 3. Configures systemd services (but does not start or enable them)

# To start all observability services:
sudo systemctl start flightctl-observability.target

# For automatic startup on boot:
sudo systemctl enable flightctl-observability.target

**Note**: The installation process only configures the services but does not automatically start them. Use systemd targets to start the observability stack.
```

**Access**:

- Grafana dashboard: `http://<host>:3000` (default port, configurable)
- Prometheus metrics: Available through Grafana (internal only)
- OpenTelemetry collector: `<host>:4317` (gRPC), `<host>:4318` (HTTP)

**Access Methods**:

- **Local deployment**: `http://localhost:3000`
- **Remote deployment**: `http://server-ip:3000` or `http://server-hostname:3000`
- **Custom domain**: `http://grafana.yourdomain.com:3000` (with proper DNS/proxy setup)
- **Custom port**: Configure `published_port` in service-config.yaml

## Components

### Grafana Dashboard

**Purpose**: Web-based visualization and dashboards for metrics and logs.

**Key Features**:

- Pre-configured FlightCtl dashboards
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
      token_url: https://your-aap.com/o/token
      api_url: http://flightctl-userinfo-proxy:8080
      tls_skip_verify: false
      local_admin_user: admin
      local_admin_password: secure-password
    protocol: http  # or https
    https:
      cert_file: /etc/grafana/certs/grafana.crt
      cert_key: /etc/grafana/certs/grafana.key
```

### Prometheus Metrics

**Purpose**: Time-series database for metrics collection and storage.

**Key Features**:

- Automatic FlightCtl service discovery
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

**Note**: Prometheus configuration is automatically generated to scrape FlightCtl services and the OpenTelemetry collector.

### OpenTelemetry Collector

**Purpose**: Unified telemetry data collection, processing, and forwarding.

**Key Features**:

- Multiple protocol support (OTLP, Jaeger, Zipkin)
- Data transformation and filtering
- Export to multiple backends
- Resource detection and enrichment

**Configuration**:

- **Internal Address**: `flightctl-otel-collector:4317` (gRPC), `flightctl-otel-collector:4318` (HTTP)
- **External Access**: `<host>:4317` (gRPC), `<host>:4318` (HTTP) - configurable ports
- **Data Storage**: `/var/lib/otelcol`

**Available Options in service-config.yaml**:

```yaml
observability:
  otel_collector:
    image: docker.io/otel/opentelemetry-collector-contrib:latest
    grpc_port: 4317  # External gRPC port
    http_port: 4318  # External HTTP port
```

**Note**: OpenTelemetry collector configuration can be customized by editing `/etc/otelcol/otelcol-config.yaml` to configure receivers, processors, and exporters.

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
      token_url: https://your-idp.com/o/token
      api_url: http://flightctl-userinfo-proxy:8080  # Internal container communication
      tls_skip_verify: false  # Skip TLS verification for OAuth endpoints
      local_admin_user: admin
      local_admin_password: secure-password
    
    protocol: http  # or https
    
    # HTTPS Configuration (Optional - only needed when protocol: https)
    https:
      cert_file: /etc/grafana/certs/grafana.crt
      cert_key: /etc/grafana/certs/grafana.key

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
3. **Hot configuration**: Use `sudo flightctl-render-observability` then restart services with systemd targets
4. **Built-in validation**: The system automatically validates your configuration and provides clear error messages

## Configuration Management

### Configuration Management System

FlightCtl separates configuration management from service management for better control.

#### Configuration Rendering

**Render Configuration Templates**:

```bash
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
sudo systemctl start flightctl-otel-collector.target      # OpenTelemetry only

# Stop services  
sudo systemctl stop flightctl-observability.target        # Full stack
sudo systemctl stop flightctl-otel-collector.target       # OpenTelemetry only

# Restart services (after config changes)
sudo systemctl restart flightctl-observability.target     # Full stack
sudo systemctl restart flightctl-otel-collector.target    # OpenTelemetry only

# Enable automatic startup
sudo systemctl enable flightctl-observability.target      # Full stack
sudo systemctl enable flightctl-otel-collector.target     # OpenTelemetry only
```

These commands work whether you have the full observability stack, standalone OpenTelemetry collector, or any combination of components.

### Configuration Workflow

1. **Edit configuration**:

   ```bash
   sudo vim /etc/flightctl/service-config.yaml
   ```

2. **Render updated configuration**:

   ```bash
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
      token_url: https://your-idp.com/o/token
      api_url: http://flightctl-userinfo-proxy:8080  # Points to internal proxy
      
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
  otel_collector:
    image: docker.io/otel/opentelemetry-collector-contrib:latest
    grpc_port: 4317
    http_port: 4318
```

**Note**: Configure `/etc/otelcol/otelcol-config.yaml` to export to your external Prometheus, Jaeger, or other observability systems.

**Management Commands Available**:

Even when installing only the OpenTelemetry collector, you have access to the management commands:

- `sudo flightctl-render-observability` - Render configuration templates from `service-config.yaml`
- `sudo systemctl start/stop/restart flightctl-observability.target` - Manage observability services

**Two-step process**: First render configuration with `flightctl-render-observability`, then manage services with systemd targets. This separation provides better control and follows systemd best practices.

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

  otel_collector:
    image: docker.io/otel/opentelemetry-collector-contrib:latest
    grpc_port: 4317
    http_port: 4318
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
      token_url: https://your-aap-instance.com/o/token
      api_url: http://flightctl-userinfo-proxy:8080
      tls_skip_verify: false
      local_admin_user: admin
      local_admin_password: fallback-password

  prometheus:
    image: docker.io/prom/prometheus:latest

  otel_collector:
    image: docker.io/otel/opentelemetry-collector-contrib:latest
    grpc_port: 4317
    http_port: 4318

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
      token_url: https://your-aap-instance.com/o/token
      api_url: http://flightctl-userinfo-proxy:8080
      tls_skip_verify: false

  prometheus:
    image: docker.io/prom/prometheus:latest

  otel_collector:
    image: docker.io/otel/opentelemetry-collector-contrib:latest
    grpc_port: 4317
    http_port: 4318

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
      token_url: https://dev-auth.local/o/token
      api_url: http://flightctl-userinfo-proxy:8080
      tls_skip_verify: true  # OK for development
      local_admin_user: admin
      local_admin_password: dev-password

  prometheus:
    image: docker.io/prom/prometheus:latest

  otel_collector:
    image: docker.io/otel/opentelemetry-collector-contrib:latest

  userinfo_proxy:
    image: flightctl/userinfo-proxy:latest
    upstream_url: https://dev-aap-instance.local/api/gateway/v1/me/
    skip_tls_verify: true  # OK for development with self-signed certs
```

### External Integration Only

For integrating with existing observability infrastructure:

```yaml
observability:
  otel_collector:
    image: docker.io/otel/opentelemetry-collector-contrib:latest
    grpc_port: 4317
    http_port: 4318
```

**Note**: Customize `/etc/otelcol/otelcol-config.yaml` to export to your external Prometheus, Grafana, Jaeger, or other observability systems.

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

  otel_collector:
    image: docker.io/otel/opentelemetry-collector-contrib:latest
    grpc_port: 14317  # Use port 14317 instead of 4317
    http_port: 14318  # Use port 14318 instead of 4318
```

**Access with custom ports**:

- Grafana: `http://<host>:8080`
- OpenTelemetry collector: `<host>:14317` (gRPC), `<host>:14318` (HTTP)

## Configuration Reference

This section provides detailed documentation for every configuration variable available in the FlightCtl observability stack.

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
- **Description**: OAuth token endpoint URL. Grafana exchanges authorization codes for access tokens here.
- **Required**: When `oauth.enabled: true`
- **Example**: `https://your-aap.com/o/token`

**`observability.grafana.oauth.api_url`**

- **Type**: String (URL)
- **Default**: Empty
- **Description**: OAuth user info API endpoint. Grafana calls this to get user information. For AAP integration, use the UserInfo proxy: `http://flightctl-userinfo-proxy:8080`
- **Required**: When `oauth.enabled: true`
- **Example**: `http://flightctl-userinfo-proxy:8080`

**`observability.grafana.oauth.tls_skip_verify`**

- **Type**: Boolean
- **Default**: `false`
- **Description**: Skip TLS certificate verification when connecting to OAuth endpoints. Only use `true` in development environments with self-signed certificates.
- **Security**: Always use `false` in production
- **Example**: `true` (development only)

**`observability.grafana.oauth.local_admin_user`**

- **Type**: String
- **Default**: `admin`
- **Description**: Username for local Grafana admin account. This account can be used as fallback when OAuth is unavailable.
- **Example**: `admin`

**`observability.grafana.oauth.local_admin_password`**

- **Type**: String
- **Default**: `defaultadmin`
- **Description**: Password for local Grafana admin account. Change this from the default for security.
- **Security**: Use a strong password in production
- **Example**: `secure-admin-password-123`

### Prometheus Configuration Variables

**`observability.prometheus.image`**

- **Type**: String
- **Default**: `docker.io/prom/prometheus:latest`
- **Description**: Container image for Prometheus. Prometheus runs as internal-only service with no external ports.
- **Example**: `docker.io/prom/prometheus:v2.45.0`

### OpenTelemetry Collector Configuration Variables

**`observability.otel_collector.image`**

- **Type**: String
- **Default**: `docker.io/otel/opentelemetry-collector-contrib:latest`
- **Description**: Container image for OpenTelemetry Collector. The contrib version includes additional receivers, processors, and exporters.
- **Example**: `docker.io/otel/opentelemetry-collector-contrib:0.88.0`

**`observability.otel_collector.grpc_port`**

- **Type**: Integer
- **Default**: `4317`
- **Description**: External port for OpenTelemetry gRPC receiver. Agents send telemetry data to this port using OTLP/gRPC protocol.
- **Example**: `14317`

**`observability.otel_collector.http_port`**

- **Type**: Integer
- **Default**: `4318`
- **Description**: External port for OpenTelemetry HTTP receiver. Agents send telemetry data to this port using OTLP/HTTP protocol.
- **Example**: `14318`

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

**Development Allowances**:

- `tls_skip_verify: true` for self-signed certificates
- `skip_tls_verify: true` for internal development IdPs
- Default passwords acceptable for local development
