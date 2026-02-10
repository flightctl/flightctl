# Deploying the Observability Stack on Linux

This guide describes how to deploy and configure the observability stack for Flight Control on RHEL or other Linux distributions using Podman.

## Overview

Flight Control provides built-in device telemetry capabilities through the **Telemetry Gateway**, which is deployed by default with the Flight Control service. The Telemetry Gateway:

- Acts as the entry point for all device telemetry data
- Terminates mTLS connections from devices and validates device certificates
- Labels telemetry data with authenticated `device_id` and `org_id`
- Exposes metrics for scraping by Prometheus or forwards data to upstream OTLP backends

To store and visualize this telemetry data, along with Flight Control service metrics, you need to deploy an observability stack consisting of:

- **Prometheus** - Time-series database for storing metrics
- **Grafana** - Visualization and dashboarding platform

The Telemetry Gateway is automatically deployed with the `flightctl-services` RPM with working default configuration, including:

- Server certificates provisioned and mounted
- CA certificate for validating device client certificates
- Device-facing OTLP gRPC listener on port 4317
- Prometheus metrics export on port 9464

## Prerequisites

- RHEL 9.2 or later (or compatible Linux distribution)
- Flight Control services already installed via `flightctl-services` RPM
- Podman 4.4 or later
- SELinux in enforcing mode (recommended)

## Installing the Observability Stack

Install the `flightctl-observability` RPM package:

```console
sudo dnf install flightctl-observability
```

The package includes:

- Prometheus configuration and container
- Grafana configuration and container
- UserInfo proxy for authentication
- Systemd quadlet files for all components
- Pre-configured dashboards for Flight Control

## Starting the Observability Services

Start and enable the observability stack:

```console
sudo systemctl daemon-reload
sudo systemctl enable --now flightctl-observability.target
```

Verify all services are running:

```console
sudo systemctl status flightctl-observability.target
```

You should see the following services in active (running) state:

- `flightctl-prometheus.service`
- `flightctl-grafana.service`
- `flightctl-userinfo-proxy.service`

## Accessing Grafana

Grafana is accessible at `https://<your-host>:3000` using automatically generated TLS certificates.

Default ports:

- **Grafana**: 3000 (HTTPS)
- **Prometheus**: 9090 (HTTP, localhost only)
- **UserInfo Proxy**: 8888 (HTTP, localhost only)

The `flightctl-observability` package automatically generates self-signed TLS certificates for Grafana during installation. These certificates include Subject Alternative Names (SANs) for the hostname, IP addresses, and common internal names.

**Note**: Since the certificates are self-signed, your browser will show a security warning on first access. You can safely proceed or install the Flight Control CA certificate (`/etc/flightctl/pki/ca/ca.crt`) in your browser's trust store.

By default, Grafana uses local authentication with username `admin` and password `admin`. You should change the default password after first login or configure authentication as described in the next section.

## Configuring Grafana Authentication

The observability stack supports two authentication modes for Grafana: local authentication and OAuth integration with Ansible Automation Platform (AAP).

### Local Authentication (Default)

By default, Grafana uses local user accounts. The administrator account is configured in `/etc/flightctl/service-config.yaml`:

```yaml
observability:
  grafana:
    local_admin_user: admin
    local_admin_password: secure-password
```

To change the admin password:

1. Edit `/etc/flightctl/service-config.yaml` and update the `local_admin_password` field
2. Restart the Grafana service:

   ```console
   sudo systemctl restart flightctl-grafana.service
   ```

**Note**: Local authentication is independent from Flight Control's own authentication. These are separate credential systems.

### OAuth Integration with Ansible Automation Platform

For organizations using Ansible Automation Platform (AAP), you can configure Grafana to authenticate users via AAP's OAuth provider. This integration requires the **UserInfo Proxy** component.

#### Why the UserInfo Proxy is Necessary

AAP's OAuth2 implementation uses a custom user API endpoint (`/api/gateway/v1/me/`) that returns user information in AAP's specific format, rather than the standard OpenID Connect UserInfo format that Grafana expects. The UserInfo Proxy acts as a translation layer that:

- Accepts OAuth tokens from Grafana
- Queries AAP's user API with the token
- Transforms AAP's response format into standard OAuth2 UserInfo format
- Maps AAP user permissions (`is_superuser`, `is_platform_auditor`) to Grafana roles

Without this proxy, Grafana cannot understand AAP's user API responses and authentication will fail.

#### Configuring AAP OAuth Integration

1. Register an OAuth2 application in your AAP instance and obtain the client ID and client secret.

2. Edit `/etc/flightctl/service-config.yaml`:

   ```yaml
   observability:
     grafana:
       oauth:
         enabled: true
         client_id: your-aap-oauth-client-id
         client_secret: your-aap-oauth-client-secret
         auth_url: https://your-aap.example.com/o/authorize/
         token_url: https://your-aap.example.com/o/token/
         api_url: http://flightctl-userinfo-proxy:8888/userinfo
         scopes: read
         tls_skip_verify: false

       # Local admin account remains available as fallback
       local_admin_user: admin
       local_admin_password: fallback-password

     userinfo_proxy:
       upstream_url: https://your-aap.example.com/api/gateway/v1/me/
       skip_tls_verify: false
   ```

3. Restart the observability services:

   ```console
   sudo systemctl restart flightctl-observability.target
   ```

**Configuration Notes**:

- `api_url` must point to the UserInfo Proxy's internal endpoint: `http://flightctl-userinfo-proxy:8888/userinfo`
- `upstream_url` should point to your AAP instance's user API endpoint
- Set `skip_tls_verify: true` only for development environments with self-signed certificates
- The local admin account remains available as a fallback if OAuth becomes unavailable

#### Role Mapping

The UserInfo Proxy maps AAP user permissions to Grafana roles:

| AAP Permission | Grafana Role |
|----------------|--------------|
| `is_superuser: true` | Admin |
| `is_platform_auditor: true` | Editor |
| Neither | Viewer |

## Persistent Data

Prometheus and Grafana data is stored in:

- **Prometheus**: `/var/lib/prometheus`
- **Grafana**: `/var/lib/grafana`

These directories are automatically created with correct ownership and SELinux contexts during installation.

To preserve data across system reboots, ensure these directories are backed up regularly.

## Configuring Prometheus Scraping

The `flightctl-observability` package includes a pre-configured Prometheus configuration at `/etc/flightctl/flightctl-prometheus/prometheus.yml` that automatically scrapes:

- **Flight Control API metrics** at `flightctl-api:9090/metrics`
- **Telemetry Gateway metrics** at `flightctl-telemetry-gateway:9464/metrics`

No additional configuration is required for basic setup.

### Available Service Metrics

The Flight Control API exposes metrics including:

- **HTTP metrics**: Request duration, status codes, request/response sizes
- **System metrics**: CPU utilization, memory usage, disk I/O
- **Database metrics**: Connection pool stats, query durations
- **gRPC metrics**: RPC call durations and counts

For a complete reference, see [Metrics Configuration](../references/metrics.md).

### Understanding Device Telemetry Metrics

All device telemetry metrics include labels for filtering and aggregation:

- `device_id`: Unique device identifier
- `org_id`: Organization/tenant identifier
- Additional labels from the OpenTelemetry collector configuration

Example metrics:

- `system_cpu_utilization`: Device CPU usage percentage
- `system_memory_usage`: Device memory consumption
- `system_disk_io_bytes`: Disk I/O operations

### Example Prometheus Queries

Access Prometheus at `http://localhost:9090` and run queries:

Get CPU usage for a specific device:

```promql
system_cpu_utilization{device_id="my-device-123"}
```

Average CPU usage across all devices in an organization:

```promql
avg(system_cpu_utilization{org_id="default"})
```

Devices with high memory usage:

```promql
system_memory_usage{state="used"} / system_memory_limit > 0.9
```

## Configuring Telemetry Gateway Forwarding (Optional)

By default, the Telemetry Gateway exports metrics for local Prometheus scraping. You can optionally configure forwarding to send telemetry data to an upstream OTLP/gRPC backend.

### When to Use Forwarding

Use forwarding when you:

- Have an existing observability infrastructure (e.g., Grafana Cloud, Datadog, New Relic)
- Want to centralize telemetry from multiple Flight Control deployments
- Need to integrate with organization-wide monitoring systems

You can configure both `export` and `forward` simultaneously. The gateway will:

- Export metrics locally for Prometheus scraping **and**
- Forward the same data to the upstream OTLP endpoint

### Basic Forwarding Configuration

1. Edit `/etc/flightctl/service-config.yaml`:

   ```yaml
   telemetryGateway:
     forward:
       endpoint: otlp.example.com:4317
   ```

2. Place the upstream CA certificate:

   ```console
   sudo mkdir -p /etc/flightctl/flightctl-telemetry-gateway/forward
   sudo cp /path/to/upstream-ca.crt /etc/flightctl/flightctl-telemetry-gateway/forward/ca.crt
   sudo chown root:root /etc/flightctl/flightctl-telemetry-gateway/forward/ca.crt
   sudo chmod 644 /etc/flightctl/flightctl-telemetry-gateway/forward/ca.crt
   ```

3. Restart the telemetry gateway:

   ```console
   sudo systemctl restart flightctl-telemetry-gateway.service
   ```

### Forwarding with Mutual TLS (mTLS)

If the upstream backend requires client certificate authentication, place all certificates in the forward directory:

```console
sudo cp /path/to/upstream-ca.crt /etc/flightctl/flightctl-telemetry-gateway/forward/ca.crt
sudo cp /path/to/client-cert.crt /etc/flightctl/flightctl-telemetry-gateway/forward/client.crt
sudo cp /path/to/client-key.key /etc/flightctl/flightctl-telemetry-gateway/forward/client.key
sudo chown root:root /etc/flightctl/flightctl-telemetry-gateway/forward/*
sudo chmod 644 /etc/flightctl/flightctl-telemetry-gateway/forward/*.crt
sudo chmod 600 /etc/flightctl/flightctl-telemetry-gateway/forward/*.key
```

Restart the service:

```console
sudo systemctl restart flightctl-telemetry-gateway.service
```

### Verifying Forwarding

Check the telemetry gateway logs to verify successful forwarding:

```console
sudo journalctl -u flightctl-telemetry-gateway.service -f
```

Look for log entries indicating successful OTLP exports:

```json
{"level":"info","msg":"Successfully forwarded metrics batch","endpoint":"otlp.example.com:4317","batch_size":100}
```

## Troubleshooting

### Services Not Starting

Check service status and logs:

```console
sudo systemctl status flightctl-prometheus.service
sudo journalctl -u flightctl-prometheus.service -n 50
```

Common issues:

- **SELinux denials**: Check `ausearch -m avc -ts recent` for denials
- **Port conflicts**: Ensure ports 3000, 9090, 8888 are available
- **Storage permissions**: Verify `/var/lib/prometheus` and `/var/lib/grafana` have correct ownership

### Cannot Access Grafana

1. Verify the service is running:

   ```console
   sudo systemctl status flightctl-grafana.service
   ```

2. Check firewall rules:

   ```console
   sudo firewall-cmd --list-ports
   sudo firewall-cmd --add-port=3000/tcp --permanent
   sudo firewall-cmd --reload
   ```

3. Verify network connectivity:

   ```console
   curl -k https://localhost:3000
   ```

### Prometheus Not Scraping Metrics

1. Verify the configuration:

   ```console
   cat /etc/flightctl/flightctl-prometheus/prometheus.yml
   ```

2. Check Prometheus targets at `http://localhost:9090/targets`

3. Verify Flight Control services are running:

   ```console
   sudo systemctl status flightctl.target
   ```

## Next Steps

- **Add devices**: See [Adding OpenTelemetry Collector to Devices](../building/building-images.md#optional-adding-opentelemetry-collector-to-devices) to configure devices to send telemetry
- **Create dashboards**: Import pre-built Grafana dashboards or create custom ones
- **Configure alerts**: Set up alerting rules in Prometheus and Alertmanager (see [Alerts and Monitoring](../references/alerts.md))
