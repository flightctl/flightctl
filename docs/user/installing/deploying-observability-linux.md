# Deploying an Observability Stack on RHEL / Linux

This guide describes how to deploy and configure an observability stack for Flight Control on RHEL or other RPM-based Linux distributions.

## Overview

The Flight Control service exports three categories of metrics:

1. **Service metrics** are metrics about the Flight Control service itself such as
    - system resource utilization metrics,
    - API metrics,
    - database metrics, and
    - agent metrics.
2. **Business metrics** are metrics about the objects managed by the Flight Control service such as
    - device deployment, update, and health metrics,
    - application deployment, update, and health metrics, and
    - fleet rollout metrics.
3. **Device and application metrics** are metrics collected from the devices and their application workloads.

The first two categories can be collected from the Flight Control API server, the third from the Flight Control Telemetry Gateway. The Telemetry Gateway

- acts as the entry point for all device telemetry data,
- terminates the OpenTelemetry mTLS connections from devices and validates the client certificates,
- labels telemetry data with authenticated `device_id` and `org_id`, and
- exposes metrics for scraping by Prometheus or forwards data to upstream OTLP backends.

To store and visualize this telemetry data, you can deploy an observability stack as described in the following sections.

## Deploying the Observability Stack

Prerequisites:

- You are logged in to the host machine running RHEL 9.7 or later (or compatible Linux distribution)
- Flight Control services are installed via the `flightctl-services` RPM package

Procedure:

1. Install the `flightctl-observability` RPM package:

   ```console
   sudo dnf install -y flightctl-observability
   ```

2. Start and enable the observability stack:

   ```console
   sudo systemctl enable --now flightctl-observability.target
   ```

Verification:

1. Verify all services are running:

   ```console
   sudo systemctl status flightctl-observability.target
   ```

   You should see the following services in active (running) state:
   - `flightctl-prometheus.service`
   - `flightctl-grafana.service`
   - `flightctl-userinfo-proxy.service`

2. Verify that Prometheus is successfully scraping Flight Control metrics:

   ```console
   sudo podman exec flightctl-prometheus wget -qO- http://localhost:9090/api/v1/targets 2>/dev/null | \
     jq -r '.data.activeTargets[] | select(.labels.job | contains("flightctl")) | {job: .labels.job, health: .health}'
   ```

   You should see output showing healthy targets for both the API and Telemetry Gateway.

3. Access the Grafana UI at `https://<your-host>:3000` with username `admin` and password `admin` (note that older installations used the default password `defaultadmin`).

4. Navigate to **Dashboards** in the left sidebar. You should see:
   - **Flight Control API Dashboard** - Shows API server metrics, database performance, and agent connectivity
   - **Flight Control Fleet Dashboard** - Shows device fleet health, rollout status, and application metrics

Troubleshooting:

1. **Services not starting** - Check service status and logs:

   ```console
   sudo systemctl status flightctl-prometheus.service
   sudo journalctl -u flightctl-prometheus.service -n 50
   ```

   Common issues:
   - **SELinux denials**: Check `ausearch -m avc -ts recent` for denials
   - **Port conflicts**: Ensure ports 3000, 9090, 8888 are available
   - **Storage permissions**: Verify `/var/lib/prometheus` and `/var/lib/grafana` have correct ownership

2. **Prometheus not scraping metrics** - Verify the configuration and check targets:

   ```console
   cat /etc/flightctl/flightctl-prometheus/prometheus.yml
   ```

   Check Prometheus targets and verify Flight Control services are running:

   ```console
   sudo podman exec flightctl-prometheus wget -qO- http://localhost:9090/api/v1/targets 2>/dev/null | jq .
   sudo systemctl status flightctl.target
   ```

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

## Configuring Telemetry Gateway Forwarding (Optional)

By default, the Telemetry Gateway exports metrics for local Prometheus scraping. You can optionally configure forwarding to send telemetry data to an upstream OTLP/gRPC backend.

Use forwarding when you:

- Have an existing observability infrastructure (e.g., Grafana Cloud, Datadog, New Relic)
- Want to centralize telemetry from multiple Flight Control deployments
- Need to integrate with organization-wide monitoring systems

To configure forwarding with Mutual TLS (mTLS), follow the procedure below.

Procedure:

1. Place the client certificate and key in the forward directory:

   ```console
   sudo install -o root -g root -m 644 /path/to/client-cert.crt \
     /etc/flightctl/flightctl-telemetry-gateway/forward/client.crt
   sudo install -o root -g root -m 600 /path/to/client-key.key \
     /etc/flightctl/flightctl-telemetry-gateway/forward/client.key
   ```

2. Place the upstream CA certificate:

   ```console
   sudo install -o root -g root -m 644 /path/to/upstream-ca.crt \
     /etc/flightctl/flightctl-telemetry-gateway/forward/ca.crt
   ```

3. Edit `/etc/flightctl/service-config.yaml` to configure the forwarding endpoint:

   ```yaml
   telemetryGateway:
     forward:
       endpoint: otlp.example.com:4317
       tls:
         insecureSkipTlsVerify: false
         caFile: /etc/flightctl/flightctl-telemetry-gateway/forward/ca.crt
         certFile: /etc/flightctl/flightctl-telemetry-gateway/forward/client.crt
         keyFile: /etc/flightctl/flightctl-telemetry-gateway/forward/client.key
   ```

4. Restart the telemetry gateway:

   ```console
   sudo systemctl restart flightctl-telemetry-gateway.service
   ```

Verification:

1. Check the telemetry gateway logs to verify successful forwarding:

   ```console
   sudo journalctl -u flightctl-telemetry-gateway.service -f
   ```

2. Look for log entries indicating successful OTLP exports:

   ```json
   {"level":"info","msg":"Successfully forwarded metrics batch","endpoint":"otlp.example.com:4317","batch_size":100}
   ```

## Next Steps

- **Add devices**: See [Adding OpenTelemetry Collector to Devices](../building/building-images.md#optional-adding-opentelemetry-collector-to-devices) to configure devices to send telemetry
