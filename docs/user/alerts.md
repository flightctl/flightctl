# Flight Control Alerts

Flight Control provides built-in alerting capabilities that automatically monitor your edge devices and notify you when issues occur. This system processes events from your devices and forwards them to Alertmanager for distribution.

## Overview

The Flight Control alerting system consists of three main components:

1. **Alert Exporter**: Processes Flight Control events and converts them to Prometheus-compatible alerts
2. **Alertmanager Proxy**: Provides authenticated access to Alertmanager using Flight Control credentials
3. **Alertmanager**: Handles alert routing, grouping, silencing, and notification delivery

## Supported Alert Types

Flight Control automatically generates alerts for the following conditions:

### Device Status Alerts

- **Device Disconnected**: Triggered when a device loses connection to Flight Control
- **Device Connected**: Automatically resolves disconnection alerts when devices reconnect

### Resource Usage Alerts

- **CPU Alerts**:
  - `DeviceCPUCritical`: CPU usage exceeds critical threshold
  - `DeviceCPUWarning`: CPU usage exceeds warning threshold
  - `DeviceCPUNormal`: Resolves CPU alerts when usage returns to normal

- **Memory Alerts**:
  - `DeviceMemoryCritical`: Memory usage exceeds critical threshold
  - `DeviceMemoryWarning`: Memory usage exceeds warning threshold
  - `DeviceMemoryNormal`: Resolves memory alerts when usage returns to normal

- **Disk Alerts**:
  - `DeviceDiskCritical`: Disk usage exceeds critical threshold
  - `DeviceDiskWarning`: Disk usage exceeds warning threshold
  - `DeviceDiskNormal`: Resolves disk alerts when usage returns to normal

### Application Alerts

- **Application Status**:
  - `DeviceApplicationError`: Application is in error state
  - `DeviceApplicationDegraded`: Application is running but degraded
  - `DeviceApplicationHealthy`: Resolves application alerts when healthy

### Lifecycle Alerts

- **Resource Deletion**: Automatically resolves all alerts when a device or resource is deleted
- **Device Decommissioning**: Resolves all alerts when a device is decommissioned

## Alert States

### Active Alerts

Alerts are considered **active** when:

- The triggering condition is present (e.g., device disconnected, high CPU usage)
- No resolving event has been received
- The device/resource still exists

### Resolved Alerts

Alerts are automatically **resolved** when:

- A resolving event is received (e.g., `DeviceConnected`, `DeviceCPUNormal`)
- The device or resource is deleted
- The device is decommissioned

## Accessing Alerts

### Prerequisites

Flight Control alerts are accessible through the Alertmanager proxy, which requires authentication. You'll need:

1. **Valid Flight Control credentials** (token-based authentication)
2. **Network access** to the Flight Control deployment
3. **Proper permissions** to view alerts for your organization/devices

### Authentication Setup

1. **Get your authentication token** (varies by auth method):
   - **OIDC**: Obtain token from your identity provider or `flightctl` client config:

     ```bash
     # Extract token from flightctl client config
     TOKEN=$(grep '^  token:' ~/.config/flightctl/client.yaml | awk '{print $2}')
     ```

   - **OpenShift**: Use `oc whoami -t` command
   - **AAP**: Use your AAP Gateway token

2. **Access the Web UI**:
   - **In your browser**, navigate to the alertmanager proxy URL:
     - External: `https://flightctl-alertmanager-proxy.<your-domain>:8443`
     - Example: `https://flightctl-alertmanager-proxy.example.com:8443`
   - **Note**: Browser-based authentication with bearer tokens requires additional setup (like browser extensions or proxy tools)
   - **Alternative**: Use API access (see examples below) or tools like curl for programmatic access

3. **Test connectivity** (optional):

   ```bash
   # Test that the proxy is accessible and your token works
   curl -H "Authorization: Bearer <your-token>" \
        https://flightctl-alertmanager-proxy.<your-domain>:8443/api/v2/status
   ```

#### Practical Examples

**For Web UI Access:**

```bash
# Your alertmanager proxy URL (replace with your domain):
# https://flightctl-alertmanager-proxy.<your-domain>:8443
```

**For API/Programmatic Access:**

```bash
# Extract your flightctl token
TOKEN=$(grep '^  token:' ~/.config/flightctl/client.yaml | awk '{print $2}')

# Test connection
curl -H "Authorization: Bearer $TOKEN" \
     https://flightctl-alertmanager-proxy.<your-domain>:8443/api/v2/status

# Get all alerts
curl -H "Authorization: Bearer $TOKEN" \
     https://flightctl-alertmanager-proxy.<your-domain>:8443/api/v2/alerts
```

### Using the API

Query active alerts programmatically:

```bash
# List all active alerts (external access)
curl -H "Authorization: Bearer <your-token>" \
     https://flightctl-alertmanager-proxy.<your-domain>:8443/api/v2/alerts

# Filter alerts by device
curl -H "Authorization: Bearer <your-token>" \
     "https://flightctl-alertmanager-proxy.<your-domain>:8443/api/v2/alerts?filter=resource=my-device"

# Filter alerts by type
curl -H "Authorization: Bearer <your-token>" \
     "https://flightctl-alertmanager-proxy.<your-domain>:8443/api/v2/alerts?filter=alertname=DeviceDisconnected"
```

## Configuration

### Alert Polling Interval

Configure how frequently Flight Control checks for new events to generate alerts:

```yaml
# In your Flight Control configuration
service:
  alertPollingInterval: "30s"  # Check for new events every 30 seconds
```

### Alertmanager Integration

Flight Control automatically connects to Alertmanager when deployed. The connection is configured via:

```yaml
# In your Flight Control configuration
alertmanager:
  hostname: "flightctl-alertmanager"
  port: 9093
```

### Enabling/Disabling Components

Control which alert components are deployed:

```yaml
# In Helm values.yaml
alertExporter:
  enabled: true  # Set to false to disable alert generation

alertmanagerProxy:
  enabled: true  # Set to false to disable authenticated access

alertmanager:
  enabled: true  # Set to false to disable Alertmanager entirely
```

## Notification Setup

Flight Control uses Prometheus Alertmanager for notifications. Configure notification channels in your Alertmanager configuration:

### Email Notifications

```yaml
# alertmanager.yml
global:
  smtp_smarthost: 'smtp.example.com:587'
  smtp_from: 'flightctl@example.com'

route:
  group_by: ['alertname']
  group_wait: 10s
  group_interval: 10s
  repeat_interval: 1h
  receiver: 'email-notifications'

receivers:
- name: 'email-notifications'
  email_configs:
  - to: 'admin@example.com'
    subject: 'Flight Control Alert: {{ .GroupLabels.alertname }}'
    body: |
      {{ range .Alerts }}
      Alert: {{ .Annotations.summary }}
      Device: {{ .Labels.resource }}
      {{ end }}
```

### Slack Notifications

```yaml
# alertmanager.yml
receivers:
- name: 'slack-notifications'
  slack_configs:
  - api_url: 'YOUR_SLACK_WEBHOOK_URL'
    channel: '#flightctl-alerts'
    title: 'Flight Control Alert'
    text: |
      {{ range .Alerts }}
      *{{ .Labels.alertname }}* on device {{ .Labels.resource }}
      {{ end }}
```

### Webhook Notifications

```yaml
# alertmanager.yml
receivers:
- name: 'webhook-notifications'
  webhook_configs:
  - url: 'http://your-webhook-endpoint.com/alerts'
    send_resolved: true
```

## Alert Labels and Filtering

Every Flight Control alert includes these labels:

- `alertname`: The type of alert (e.g., "DeviceDisconnected")
- `resource`: The name of the affected resource
- `org_id`: The organization ID

Use these labels to create targeted notification rules and filters:

```yaml
# Route critical CPU alerts to on-call team
routes:
- match:
    alertname: DeviceCPUCritical
  receiver: 'oncall-team'

# Route disconnection alerts to monitoring team
- match:
    alertname: DeviceDisconnected
  receiver: 'monitoring-team'
```

## Troubleshooting

### No Alerts Appearing

1. **Check alert exporter status**:

   ```bash
   # For Kubernetes
   kubectl logs deployment/flightctl-alert-exporter

   # For Quadlets
   sudo journalctl -u flightctl-alert-exporter.service
   ```

2. **Check Alertmanager status**:

   ```bash
   # For Kubernetes
   kubectl logs deployment/flightctl-alertmanager

   # For Quadlets
   sudo journalctl -u flightctl-alertmanager.service
   ```

3. **Check authentication and proxy**:

   ```bash
   # Verify your token works and proxy is accessible
   curl -H "Authorization: Bearer <your-token>" \
        https://flightctl-alertmanager-proxy.<your-domain>:8443/api/v2/status
   ```

### Alerts Not Resolving

1. **Check if resolution events are being generated**:
   - Verify devices are reconnecting
   - Confirm resource usage has returned to normal
   - Check application health status

2. **Review event logs**:

   ```bash
   # Check recent events
   flightctl get events --limit 50
   ```

### Missing Alert Notifications

1. **Verify Alertmanager configuration**:

   ```bash
   # Check Alertmanager status through the proxy
   curl -H "Authorization: Bearer <your-token>" \
        https://flightctl-alertmanager-proxy.<your-domain>:8443/api/v2/status
   ```

2. **Test notification channels**:
   - Send test alerts to verify email/Slack/webhook configuration
   - Check Alertmanager logs for delivery errors

3. **Review routing rules**:
   - Ensure alert labels match your routing configuration
   - Verify receiver configurations are correct

### Performance Issues

1. **Adjust polling interval** if system is under heavy load:

   ```yaml
   service:
     alertPollingInterval: "60s"  # Reduce frequency
   ```

2. **Monitor alert exporter resource usage**:

   ```bash
   # Check memory and CPU usage
   kubectl top pod -l flightctl.service=flightctl-alert-exporter
   ```

## Best Practices

### Alert Routing Strategy

1. **Prioritize by severity**:
   - Route critical alerts (CPU/Memory critical, disconnections) to immediate notification channels
   - Route warning alerts to monitoring dashboards or delayed notifications

2. **Group by device or fleet**:
   - Avoid alert storms by grouping related alerts
   - Use appropriate group intervals to batch notifications

### Retention and Cleanup

1. **Configure alert retention**:

   ```yaml
   # In Alertmanager configuration
   global:
     resolve_timeout: 5m  # Auto-resolve alerts after 5 minutes of no updates
   ```

2. **Regular maintenance**:
   - Monitor alert volume and adjust thresholds if needed
   - Review and update notification channels regularly

### Integration with Monitoring

1. **Combine with metrics**: Use alerts alongside Flight Control metrics for comprehensive monitoring
2. **Dashboard integration**: Display alert status in monitoring dashboards
3. **Incident management**: Integrate alerts with your incident response tools

## Examples

### View All Active Alerts

```bash
curl -H "Authorization: Bearer <token>" \
     https://flightctl-alertmanager-proxy.<your-domain>:8443/api/v2/alerts | jq '.'
```

### Check Specific Device Alerts

```bash
curl -H "Authorization: Bearer <token>" \
     "https://flightctl-alertmanager-proxy.<your-domain>:8443/api/v2/alerts?filter=resource=my-edge-device" | jq '.'
```

### Monitor Alert Count

```bash
# Count active alerts
curl -s -H "Authorization: Bearer <token>" \
     https://flightctl-alertmanager-proxy.<your-domain>:8443/api/v2/alerts | jq 'length'
```
