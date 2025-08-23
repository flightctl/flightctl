# Non-Blocking Image Prefetch Management

Flight Control provides a non-blocking prefetch management system that improves the reliability of OS and application updates by intelligently downloading OCI images and artifacts
in the background. This system helps reduce update times and provides better retry handling for large image downloads.

## Overview

The prefetch management system provides:

1. **Background Downloads**: OS images and application artifacts are downloaded in the background without blocking device operations
2. **Retry Logic**: Failed downloads are automatically retried with exponential backoff
3. **Partial Download Cleanup**: Failed partial downloads are automatically cleaned up to prevent disk space exhaustion
4. **Progress Reporting**: Regular status updates during long-running downloads

This documentation covers how to monitor and troubleshoot the prefetch system.

## Understanding the Prefetch System

The prefetch manager is automatically integrated into the Flight Control agent and operates transparently during device updates. When the agent receives a new device specification, it performs the following steps:

1. **Identifies Required Images**: Determines which OS images and application artifacts need to be downloaded
2. **Schedules Downloads**: Queues downloads in the background prefetch manager
3. **Monitors Progress**: Tracks download progress and handles retries
4. **Reports Status**: Provides status updates through device conditions
5. **Manages Cleanup**: Automatically cleans up failed partial downloads

### Prefetch Status Indicators

You can monitor prefetch progress through device status conditions and update states. See [Monitoring Prefetch Status](#monitoring-prefetch-status) for detailed examples and status message descriptions.

## Download Management Features

### Background Processing

The prefetch manager operates in the background without blocking device operations:

- **Non-blocking Downloads**: Images download while the device continues normal operations
- **Queue Management**: Multiple downloads are queued and processed serially to avoid resource contention
- **Timeout Handling**: Configurable timeouts prevent hung downloads
- **Error Handling**: Network errors and timeouts are treated as retryable conditions

### Automatic Retry Logic

When downloads fail, the system automatically retries with intelligent backoff:

- **Exponential Backoff**: Retry delays increase progressively to avoid overwhelming networks
- **Transient Error Recovery**: Network-related errors trigger automatic retries
- **Persistent Error Reporting**: Non-retryable errors are reported immediately

### Partial Download Cleanup

To prevent disk space exhaustion from failed downloads:

- **Automatic Cleanup**: Failed partial downloads are automatically removed
- **Storage Protection**: Prevents accumulation of incomplete image layers
- **Resource Conservation**: Frees up space for subsequent download attempts

## Configuration and Timeout Settings

### Pull Timeout Configuration

The prefetch system respects the agent's configured pull timeout:

```yaml
# /etc/flightctl/config.yaml
pull-timeout: 20m
```

This timeout applies to:

- OS image downloads
- Application container image downloads
- OCI artifact downloads for application volumes

> See [configuring-agent.md](configuring-agent.md) for the full list of agent settings and defaults.

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
      message: "Downloading OS image in background"
```

Common prefetch status messages:

- `"Downloading OS image in background"` - OS image download in progress  
- `"2/3 images complete, pending: image1, image2"` - Multiple downloads with progress tracking
- `"Ready to apply update"` - All required images have been downloaded

## Best Practices

### Download Optimization

1. **Appropriate Timeouts**: Configure pull timeouts based on your network conditions and image sizes:

   ```yaml
   # For slow networks or large images
   pull-timeout: 30m

   # For fast networks or smaller images
   pull-timeout: 10m
   ```

2. **Network Considerations**:
    - Schedule downloads during off-peak hours when possible
    - Account for multiple devices downloading simultaneously
    - Consider bandwidth limitations at edge locations

### Disk Space Management

1. **Monitor Available Space**: Ensure adequate disk space for image downloads:
    - OS images can be 1-2 GB or larger
    - Application images add additional space requirements
    - Allow for temporary space during downloads

2. **Regular Cleanup**: Clean up unused images periodically:

   ```bash
   podman system prune -a
   ```

   > [!WARNING]
   > This removes all unused images, including successfully downloaded images awaiting deployment.

3. **Storage Planning**: Plan storage capacity for:
    - Current OS image
    - New OS image during updates
    - Application container images
    - Temporary download space

## Troubleshooting

### Common Issues and Solutions

#### Update Stuck in "Preparing" State

**Symptom**: Device shows `Preparing` state for extended periods

**Potential Causes**:

- Large OS image downloads taking time to complete
- Network connectivity issues during download
- Insufficient disk space preventing download completion
- Download timeouts causing repeated retries

**Resolution**:

1. Check agent logs for download progress:

   ```bash
   journalctl -u flightctl-agent | grep -i prefetch
   ```

2. Check available disk space:

   ```bash
   df -h
   ```

3. Verify network connectivity to registry:

   ```bash
   ping registry.example.com
   ```

4. Check if images are being downloaded:

   ```bash
   podman images
   ```

#### Prefetch Progress Status

**Symptom**: Updates show prefetch progress messages like "2/3 images complete, pending: image1, image2"

**Potential Causes**:

- Downloads still in progress
- Download failures requiring retry
- Network issues preventing downloads

**Resolution**:

1. Wait for current downloads to complete (check logs for progress)
2. Clean up disk space if low:

   ```bash
   podman system prune -a
   ```

   > [!WARNING]
   > This command removes all unused images, including successfully downloaded images that haven't been applied yet. Those images will need to be re-downloaded.

3. Verify network connectivity and registry reachability:

   ```bash
   ping registry.example.com
   ```

4. As a last resort, restart the agent to retry failed downloads:

   > [!WARNING]
   > Restarting the agent may interrupt workloads and restart applications managed on the device.
   > Use only after verifying space and connectivity, and during a maintenance window.

   ```bash
   systemctl restart flightctl-agent
   ```

#### Partial Download Cleanup Issues

**Symptom**: Disk space fills up with incomplete downloads

**Resolution**:

1. The system should automatically clean up partial downloads, but if needed:

   ```bash
   podman system reset  # Warning: removes all images
   ```

2. Check pull timeout configuration - very short timeouts may cause repeated partial downloads

### Diagnostic Commands

#### Check Prefetch Status

```bash
# View current device status and update conditions
flightctl get device/<device-name> -o yaml | grep -A 10 conditions

# Check local disk space
df -h

# Check container storage usage
podman system df
```

#### Monitor Agent Prefetch Activity

```bash
# View agent logs
journalctl -u flightctl-agent -f

# Check specific prefetch activity
journalctl -u flightctl-agent | grep -i prefetch

# Monitor download progress
journalctl -u flightctl-agent | grep -Ei 'downloading|oci target'
```

#### Container Image Management

```bash
# List all downloaded images
podman images

# Check image download history
podman history <image-name>

# Clean up unused images and free space (WARNING: removes downloaded images)
podman system prune -a

# Reset all container storage (drastic - removes everything)
podman system reset
```

## Configuration Examples

### Agent Configuration for Download Optimization

Configure the Flight Control agent for optimal download performance:

```yaml
# /etc/flightctl/config.yaml
enrollment-service:
  server: https://flightctl.example.com
  ca-cert-path: /etc/flightctl/ca.crt

# Optimize for large OS image downloads
pull-timeout: 30m

# Standard intervals (prefetch happens automatically)
spec-fetch-interval: 60s
status-update-interval: 60s
```

### Device with Scheduled Updates

Combine prefetch with scheduled updates for predictable maintenance windows:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: Device
metadata:
  name: production-device
spec:
  updatePolicy:
    downloadSchedule:
      at: "0 2 * * *"              # Download at 2 AM daily
      timeZone: "UTC"
      startGraceDuration: "2h"     # Allow 2-hour download window
    updateSchedule:
      at: "0 4 * * 0"              # Apply updates Sunday at 4 AM
      timeZone: "UTC"
      startGraceDuration: "1h"     # Allow 1-hour update window
  os:
    image: quay.io/flightctl/rhel:9.5
```

## Fleet-Wide Configuration

Configure prefetch and download scheduling across multiple devices using fleets:

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
          at: "0 2 * * *"              # Staggered downloads at 2 AM
          timeZone: "UTC"              # Explicit timezone
          startGraceDuration: "4h"     # Long window for large downloads
        updateSchedule:
          at: "0 4 * * 0"              # Apply updates Sunday at 4 AM  
          timeZone: "UTC"              # Explicit timezone
          startGraceDuration: "2h"     # Allow time for updates
      os:
        image: quay.io/flightctl/rhel:9.5
```

This configuration ensures that:

- All devices in the fleet download updates during a designated maintenance window
- Downloads have adequate time to complete before the update window
- Updates are applied consistently across the fleet

## Advanced Topics

### Staggered Fleet Downloads

To prevent overwhelming your network infrastructure, stagger downloads across device fleets:

```yaml
# European devices - download at 2 AM local time
apiVersion: flightctl.io/v1alpha1
kind: Fleet
metadata:
  name: europe-fleet
spec:
  selector:
    matchLabels:
      region: europe
  template:
    spec:
      updatePolicy:
        downloadSchedule:
          at: "0 2 * * *"
          timeZone: "Europe/London"
          startGraceDuration: "3h"
```

```yaml
# US devices - download at 2 AM local time (8 hours later)
apiVersion: flightctl.io/v1alpha1
kind: Fleet
metadata:
  name: us-fleet
spec:
  selector:
    matchLabels:
      region: us
  template:
    spec:
      updatePolicy:
        downloadSchedule:
          at: "0 2 * * *"
          timeZone: "America/New_York"
          startGraceDuration: "3h"
```

### Monitoring Fleet Prefetch Status

Monitor prefetch performance across your device fleet:

```bash
# Check devices with pending downloads
flightctl get devices -o yaml | grep -E -B 5 -A 5 'Preparing|prefetch'

# View devices by update status
flightctl get devices -o wide

# Monitor specific device download progress
flightctl get device/<device-name> -o yaml | grep -A 10 conditions
```

The non-blocking prefetch management system ensures reliable image downloads and improves update success rates by handling large bootc images and network connectivity issues
gracefully.
