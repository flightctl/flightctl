# Flight Control Metrics Configuration

This document explains how to configure and use the metrics system in Flight Control. Metrics provide visibility into the health, performance, and status of your Flight Control deployment and managed devices.

## Overview

Flight Control exposes Prometheus-compatible metrics that you can use to monitor:
- System performance and health
- Device status and fleet management
- API request patterns and performance
- Repository and resource synchronization
- Alert processing and notification delivery

## Metrics Configuration

Flight Control uses a structured metrics configuration system that allows you to control which metrics are collected and how often they're sampled.

### Basic Configuration

The metrics configuration is defined in your Flight Control service configuration file. Here's a basic example:

```yaml
metrics:
  enabled: true                    # Enable/disable all metrics collection
  address: ":15690"               # Port where metrics are exposed
```

### Advanced Configuration

For more control, you can configure individual metric collectors:

```yaml
metrics:
  enabled: true
  address: ":15690"
  
  # System metrics (CPU, memory, disk, network)
  systemCollector:
    enabled: true
    tickerInterval: 5s            # How often to collect system metrics
    
  # HTTP request metrics (API performance)
  httpCollector:
    enabled: true
    sloMax: 4.0                   # Service Level Objective threshold (seconds)
    apiLatencyBins:               # Latency histogram buckets
      - 1e-7
      - 1e-6
      - 1e-5
      - 1e-4
      - 1e-3
      - 1e-2
      - 1e-1
      - 1e0
      
  # Device status and health metrics
  deviceCollector:
    enabled: true
    tickerInterval: 30s           # How often to collect device metrics
    groupByFleet: true            # Group device metrics by fleet
    # ⚠️ Warning: groupByFleet can cause Prometheus cardinality issues
    # when you have many fleets. Consider setting to false if you have
    # more than 100-200 fleets to avoid performance problems.
    
  # Fleet management metrics
  fleetCollector:
    enabled: true
    tickerInterval: 30s           # How often to collect fleet metrics
    
  # Repository status metrics
  repositoryCollector:
    enabled: true
    tickerInterval: 30s           # How often to collect repository metrics
    
  # Resource synchronization metrics
  resourceSyncCollector:
    enabled: true
    tickerInterval: 30s           # How often to collect sync metrics
```

## Available Metrics

### System Metrics

System metrics provide insight into the Flight Control service performance:

- **CPU Usage**: Current CPU utilization
- **Memory Usage**: Memory consumption and availability
- **Disk Usage**: Storage utilization and I/O performance
- **Network Statistics**: Network throughput and connection status
- **Process Information**: Service health and resource usage

### HTTP Metrics

HTTP metrics track API performance and usage patterns:

- **Request Latency**: Response times for API endpoints
- **Request Counts**: Number of requests by endpoint and method
- **Error Rates**: Failed requests and error types
- **SLO Compliance**: Requests that exceed performance thresholds

### Device Metrics

Device metrics show the status and health of managed devices:

- **Device Status**: Online, offline, degraded, or error states
- **Application Health**: Application status across devices
- **System Updates**: Update progress and status
- **Fleet Distribution**: How devices are distributed across fleets

**⚠️ Cardinality Warning**: When `groupByFleet: true` is enabled, device metrics create separate time series for each fleet. This can cause Prometheus cardinality issues if you have many fleets (10000+). Consider setting `groupByFleet: false` for large deployments to avoid performance problems.

### Fleet Metrics

Fleet metrics provide fleet-level insights:

- **Fleet Size**: Number of devices in each fleet
- **Fleet Health**: Overall health status of fleets
- **Update Progress**: Fleet-wide update status
- **Configuration Status**: Fleet configuration deployment status

### Repository Metrics

Repository metrics track configuration source health:

- **Repository Status**: Git repository connectivity and health
- **Sync Status**: Configuration synchronization status
- **Update Frequency**: How often configurations are updated
- **Error Rates**: Repository access and sync failures

### Resource Sync Metrics

Resource sync metrics monitor configuration deployment:

- **Sync Progress**: Resource synchronization status
- **Update Frequencies**: How often resources are updated
- **Error Rates**: Sync failures and retry attempts
- **Deployment Status**: Configuration deployment progress

## Accessing Metrics

### Metrics Endpoint

Once configured, metrics are available at the configured address (default: `:15690`):

```bash
# Get metrics in Prometheus format
curl http://your-flightctl-server:15690/metrics
```

### Integration with Monitoring Systems

Flight Control metrics can be integrated with various monitoring systems:

#### Prometheus

Add Flight Control to your Prometheus configuration:

```yaml
scrape_configs:
  - job_name: 'flightctl'
    static_configs:
      - targets: ['your-flightctl-server:15690']
    metrics_path: /metrics
    scrape_interval: 15s
```