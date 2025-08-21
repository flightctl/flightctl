# Resource-Aware Update Control

Flight Control provides intelligent resource monitoring and update control capabilities to improve the reliability of OS updates by preventing and canceling updates when the system
is under critical resource pressure. This feature helps ensure that updates don't fail due to insufficient storage, memory, or CPU resources.

## Overview

The resource-aware update control system consists of several integrated components:

1. **Resource Monitoring**: Continuous monitoring of CPU, memory, and disk usage with configurable thresholds
2. **Immediate Alert Propagation**: Real-time status updates when resource alerts are triggered
3. **Prefetch Management**: Intelligent scheduling and management of OS image downloads
4. **Layer Size Analysis**: Pre-download analysis of OCI image layers to estimate storage requirements
5. **Update Control**: Prevention of updates when resources are insufficient

This documentation covers how to configure and use these capabilities effectively.

## Resource Monitoring Configuration

### Setting Up Resource Monitors

Resource monitors can be configured for CPU, memory, and disk resources. Each monitor defines thresholds that trigger alerts when resource usage exceeds specified levels.

#### Basic Disk Monitoring

To monitor disk usage and prevent updates when storage is low:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: Device
metadata:
  name: my-device
spec:
  resources:
    - monitorType: Disk
      samplingInterval: 30s
      path: /
      alertRules:
        - severity: Warning
          duration: 5m
          percentage: 80
          description: "Root filesystem >80% full for over 5 minutes"
        - severity: Critical
          duration: 2m
          percentage: 90
          description: "Root filesystem >90% full for over 2 minutes - updates may fail"
```

#### Monitoring Multiple Paths

You can configure separate monitors for different filesystem paths:

```yaml
spec:
  resources:
    - monitorType: Disk
      samplingInterval: 30s
      path: /
      alertRules:
        - severity: Critical
          duration: 2m
          percentage: 85
    - monitorType: Disk
      samplingInterval: 30s
      path: /var/lib/containers
      alertRules:
        - severity: Warning
          duration: 5m
          percentage: 75
        - severity: Critical
          duration: 2m
          percentage: 85
```

#### CPU and Memory Monitoring

```yaml
spec:
  resources:
    - monitorType: CPU
      samplingInterval: 30s
      alertRules:
        - severity: Warning
          duration: 10m
          percentage: 80
        - severity: Critical
          duration: 5m
          percentage: 95
    - monitorType: Memory
      samplingInterval: 30s
      alertRules:
        - severity: Warning
          duration: 10m
          percentage: 85
        - severity: Critical
          duration: 5m
          percentage: 95
```

### Resource Monitor Parameters

| Parameter          | Description                                                                        | Required     |
|--------------------|------------------------------------------------------------------------------------|--------------|
| `monitorType`      | Type of resource to monitor: `Disk`, `CPU`, or `Memory`                            | Yes          |
| `samplingInterval` | How often to check resource usage (e.g., `30s`, `1m`, `5m`)                        | Yes          |
| `path`             | (Disk only) Absolute path to monitor. Monitors the filesystem containing this path | Yes for Disk |
| `alertRules`       | List of threshold rules that trigger alerts                                        | Yes          |

### Alert Rule Parameters

| Parameter     | Description                                               |
|---------------|-----------------------------------------------------------|
| `severity`    | Alert severity: `Info`, `Warning`, or `Critical`          |
| `duration`    | How long the threshold must be exceeded before triggering |
| `percentage`  | Usage threshold (0-100) that triggers the alert           |
| `description` | Human-readable description of what the alert means        |

## Understanding Update Behavior

### Update Flow with Resource Monitoring

1. **Resource Monitoring**: Monitors continuously check system resources
2. **Alert Generation**: When thresholds are exceeded, alerts are generated immediately
3. **Status Reporting**: Device status reflects resource alerts (Healthy, Warning, Critical)
4. **Prefetch Scheduling**: OS images and applications are queued for download
5. **Resource Validation**: Before downloads begin, available storage is assessed
6. **Download Management**: Large downloads may be retried if they fail due to resource constraints
7. **Update Decision**: Updates proceed only when resource conditions are acceptable

### Device Status Indicators

Resource monitoring affects several device status fields:

```yaml
status:
  resources:
    cpu: Healthy
    disk: Critical      # ← Indicates disk space is critically low
    memory: Warning     # ← Indicates memory usage is high
  summary:
    status: Degraded    # ← Overall status reflects resource issues
    info: "Disk space critically low on /"
```

## Image Prefetch and Layer Management

### Understanding Prefetch Behavior

The agent intelligently manages OS image downloads through a prefetch system:

- **Automatic Scheduling**: OS images are automatically scheduled for download when updates are available
- **Background Downloads**: Images download in the background without blocking device operations
- **Retry Logic**: Failed downloads are automatically retried with exponential backoff
- **Storage Validation**: Available storage is checked before beginning large downloads

### Large Layer Handling

Flight Control includes specific handling for large OCI image layers:

- **Layer Analysis**: Before download, the system analyzes layer sizes where possible
- **Timeout Management**: Large layers get appropriate timeout values to complete downloads
- **Partial Download Cleanup**: Failed partial downloads are cleaned up to free storage
- **Progress Reporting**: Regular status updates during long-running downloads

### Monitoring Prefetch Status

You can monitor prefetch progress through device status:

```yaml
status:
  updated:
    status: OutOfDate              # Update available but not yet applied
  conditions:
    - type: DeviceUpdating
      status: "True"
      reason: Preparing
      message: "Prefetching OS image layers: 2 of 5 completed"
```

## Best Practices

### Disk Space Management

1. **Set Conservative Thresholds**: Configure disk alerts well before space is exhausted
   ```yaml
   alertRules:
   - severity: Warning
     percentage: 70    # Early warning
   - severity: Critical
     percentage: 85    # Still allows space for cleanup
   ```

2. **Monitor Container Storage**: Monitor `/var/lib/containers` separately if using container workloads
3. **Regular Cleanup**: Implement regular cleanup of temporary files and old container images
4. **Size Planning**: Plan for OS images that may be 1-2 GB or larger

### Update Scheduling

1. **Maintenance Windows**: Use update scheduling to control when updates occur:
   ```yaml
   updatePolicy:
     downloadSchedule:
       at: "0 2 * * *"              # Download at 2 AM daily
       startGraceDuration: "2h"     # Allow 2-hour window
     updateSchedule:
       at: "0 4 * * 0"              # Update Sunday at 4 AM
       startGraceDuration: "4h"     # Allow 4-hour window
   ```

2. **Coordinate with Resource Monitoring**: Schedule updates during low-usage periods

### Monitoring and Alerting

1. **Layer Resource Monitors**: Set up multiple monitoring points:
    - Root filesystem (`/`)
    - Container storage (`/var/lib/containers`)
    - Application data directories

2. **Appropriate Sampling**: Balance monitoring frequency with system load:
   ```yaml
   samplingInterval: 60s  # Good for most systems
   # samplingInterval: 30s  # For systems with rapid changes
   # samplingInterval: 5m   # For stable systems with slow changes
   ```

## Troubleshooting

### Common Issues and Solutions

#### Update Stuck in "Preparing" State

**Symptom**: Device shows `Preparing` state for extended periods

**Potential Causes**:

- Large OS image layers taking time to download
- Insufficient disk space preventing download completion
- Network connectivity issues during download

**Resolution**:

1. Check disk space: `df -h`
2. Check agent logs: `journalctl -u flightctl-agent -f`
3. Monitor network connectivity
4. Clear space if needed and restart agent

#### "Prefetch Not Ready" Errors

**Symptom**: Updates fail with prefetch-related errors

**Resolution**:

1. Check available disk space on all monitored paths
2. Review resource alerts in device status
3. Clean up unused container images: `podman system prune -a`
4. Wait for current downloads to complete

#### Resource Alerts Not Triggering

**Symptom**: High resource usage but no alerts

**Check**:

1. Verify monitor configuration is applied
2. Check sampling interval isn't too long
3. Confirm alert thresholds are appropriate for your usage patterns
4. Review agent logs for resource monitoring errors

### Diagnostic Commands

#### Check Resource Status

```bash
# View current device status including resource alerts
flightctl get device/<device-name> -o yaml

# Check local system resources
df -h                    # Disk usage
free -h                  # Memory usage
top                      # CPU usage
```

#### Monitor Agent Behavior

```bash
# View agent logs
journalctl -u flightctl-agent -f

# Check prefetch status
journalctl -u flightctl-agent | grep -i prefetch

# Monitor resource alerts
journalctl -u flightctl-agent | grep -i "resource\|alert"
```

#### Container Storage Management

```bash
# Check container storage usage
podman system df

# Clean up unused images and containers
podman system prune -a

# List all images
podman images
```

## Configuration Examples

### Production Edge Device

Suitable for edge devices with limited storage:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: Device
metadata:
  name: edge-device
spec:
  updatePolicy:
    downloadSchedule:
      at: "0 2 * * *"
      startGraceDuration: "1h"
    updateSchedule:
      at: "0 4 * * 0"
      startGraceDuration: "2h"
  resources:
    - monitorType: Disk
      samplingInterval: 60s
      path: /
      alertRules:
        - severity: Warning
          duration: 5m
          percentage: 70
          description: "Root filesystem approaching capacity"
        - severity: Critical
          duration: 2m
          percentage: 85
          description: "Root filesystem critically low - updates may fail"
    - monitorType: Disk
      samplingInterval: 60s
      path: /var/lib/containers
      alertRules:
        - severity: Warning
          duration: 5m
          percentage: 75
          description: "Container storage approaching capacity"
        - severity: Critical
          duration: 2m
          percentage: 85
          description: "Container storage critically low"
    - monitorType: Memory
      samplingInterval: 60s
      alertRules:
        - severity: Warning
          duration: 10m
          percentage: 80
        - severity: Critical
          duration: 5m
          percentage: 90
```

### Development/Testing Device

Suitable for development environments with more resources:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: Device
metadata:
  name: dev-device
spec:
  resources:
    - monitorType: Disk
      samplingInterval: 5m
      path: /
      alertRules:
        - severity: Warning
          duration: 10m
          percentage: 80
        - severity: Critical
          duration: 5m
          percentage: 90
    - monitorType: Memory
      samplingInterval: 2m
      alertRules:
        - severity: Warning
          duration: 15m
          percentage: 85
        - severity: Critical
          duration: 10m
          percentage: 95
    - monitorType: CPU
      samplingInterval: 2m
      alertRules:
        - severity: Warning
          duration: 20m
          percentage: 85
        - severity: Critical
          duration: 10m
          percentage: 95
```

## Fleet-Wide Configuration

Apply resource monitoring to multiple devices using fleets:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: Fleet
metadata:
  name: production-fleet
spec:
  selector:
    matchLabels:
      environment: production
  template:
    spec:
      updatePolicy:
        downloadSchedule:
          at: "0 2 * * *"
          startGraceDuration: "30m"
        updateSchedule:
          at: "0 4 * * 0"
          startGraceDuration: "1h"
      resources:
        - monitorType: Disk
          samplingInterval: 60s
          path: /
          alertRules:
            - severity: Warning
              duration: 5m
              percentage: 75
            - severity: Critical
              duration: 2m
              percentage: 85
```

## Advanced Topics

### Custom Resource Thresholds by Device Type

Different device types may need different resource thresholds. Use labels to create device-specific configurations:

```yaml
# High-capacity devices
apiVersion: flightctl.io/v1alpha1
kind: Fleet
metadata:
  name: high-capacity-devices
spec:
  selector:
    matchLabels:
      device-type: high-capacity
  template:
    spec:
      resources:
        - monitorType: Disk
          samplingInterval: 60s
          path: /
          alertRules:
            - severity: Critical
              percentage: 90  # Can use more disk space
```

```yaml
# Constrained devices
apiVersion: flightctl.io/v1alpha1
kind: Fleet
metadata:
  name: constrained-devices
spec:
  selector:
    matchLabels:
      device-type: constrained
  template:
    spec:
      resources:
        - monitorType: Disk
          samplingInterval: 30s
          path: /
          alertRules:
            - severity: Critical
              percentage: 70  # Conservative threshold
```

### Integration with External Monitoring

Resource alerts are reflected in device status and can be consumed by external monitoring systems through the Flight Control API:

```bash
# Query devices with resource alerts
flightctl get devices -l '!' -o jsonpath='{.items[?(@.status.resources.disk=="Critical")].metadata.name}'

# Monitor fleet resource health
flightctl get devices -o wide | grep -E "(Critical|Warning)"
```

This comprehensive resource-aware update control system ensures your Fleet Control deployments maintain high reliability and prevent update failures due to resource constraints.
