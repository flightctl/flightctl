# Flight Control Metrics

This document describes the metrics collectors available in Flight Control and how to configure them. All collectors can be enabled/disabled independently and expose Prometheus-compatible metrics.

**Global Configuration:**

- `enabled`: Master switch to enable/disable all metrics (default: `true`)
- `address`: HTTP endpoint for metrics exposure (default: `":15690"`)

## Metrics Collectors

Each collector provides specific metrics and can be configured independently. Default values are shown below.

### System Collector

Monitors system resource usage on the Flight Control server.

**Metrics:**

- `flightctl_cpu_utilization`: CPU utilization percentage
- `flightctl_memory_utilization`: Memory consumption statistics  
- `flightctl_disk_utilization`: Disk I/O operations and performance

**Configuration:**

- `enabled`: Enable/disable system collector (default: `true`)
- `tickerInterval`: Collection frequency (default: `"5s"`)

### HTTP Collector

Tracks HTTP API performance and service level compliance.

Note: This collector uses OpenTelemetry under the hood and follows standard HTTP semantic conventions. Metrics are exported in Prometheus format.

**Features:**

- **Real-time Monitoring**: Captures HTTP request metrics as they happen
- **Standard Metrics**: Provides industry-standard HTTP observability metrics
- **Prometheus Compatible**: Exports metrics in Prometheus format for easy integration

**Metrics:**

The HTTP collector automatically generates standard OpenTelemetry HTTP server metrics, which typically include:

- `http_server_duration`: HTTP request duration histograms with labels for method, route, and status code
- `http_server_request_size`: HTTP request size histograms  
- `http_server_response_size`: HTTP response size histograms

**Labels:** All HTTP metrics include labels that allow you to filter and aggregate data by different dimensions. These labels follow standard OpenTelemetry semantic conventions:

- `http_method`: HTTP request method
- `http_scheme`: HTTP scheme  
- `http_status_code`: HTTP response status code
- `net_host_name`: Network host name
- `net_host_port`: Network host port
- `net_protocol_name`: Network protocol name
- `net_protocol_version`: Network protocol version
- `service_name`: Flight Control service name

**Configuration:**

- `enabled`: Enable/disable HTTP metrics collection (default: `true`)

### Device Collector

Monitors device status and health across your fleet.

**Metrics:**

- `flightctl_devices_summary`: Device counts by status and fleet
- `flightctl_devices_application`: Application deployment status across devices  
- `flightctl_devices_update`: System update progress and status

**Labels:** `organization_id`, `fleet`, `status`

**Configuration:**

- `enabled`: Enable/disable device metrics (default: `true`)
- `tickerInterval`: Collection frequency (default: `"30s"`)
- `groupByFleet`: Group metrics by fleet (default: `true`)

### Fleet Collector

Tracks fleet management operations and status.

**Metrics:**

- `flightctl_fleets_summary`: Fleet counts and health status
- `flightctl_fleets_device_distribution`: Device distribution across fleets

**Configuration:**

- `enabled`: Enable/disable fleet metrics (default: `true`)
- `tickerInterval`: Collection frequency (default: `"30s"`)

### Repository Collector

Monitors repository synchronization and health.

**Metrics:**

- `flightctl_repositories_sync_status`: Repository synchronization health
- `flightctl_repositories_sync_duration`: Repository sync operation timing

**Configuration:**

- `enabled`: Enable/disable repository metrics (default: `true`)
- `tickerInterval`: Collection frequency (default: `"30s"`)

### Resource Sync Collector

Tracks synchronization between Flight Control and managed devices.

**Metrics:**

- `flightctl_resourcesync_status`: Resource sync success/failure rates
- `flightctl_resourcesync_latency`: Sync operation timing
- `flightctl_resourcesync_queue_depth`: Pending synchronization queue size

**Configuration:**

- `enabled`: Enable/disable resource sync metrics (default: `true`)
- `tickerInterval`: Collection frequency (default: `"30s"`)

## Configuration Examples

### Minimal Configuration

```json
{
  "metrics": {
    "enabled": true,
    "address": ":9090"
  }
}
```

### Custom Configuration

```json
{
  "metrics": {
    "enabled": true,
    "address": "0.0.0.0:9090",
    "systemCollector": {
      "enabled": true,
      "tickerInterval": "10s"
    },
    "httpCollector": {
      "enabled": true
    },
    "deviceCollector": {
      "enabled": true,
      "tickerInterval": "1m",
      "groupByFleet": false
    },
    "fleetCollector": {
      "enabled": false
    },
    "repositoryCollector": {
      "enabled": true,
      "tickerInterval": "5m"
    },
    "resourceSyncCollector": {
      "enabled": true,
      "tickerInterval": "30s"
    }
  }
}
```

## Usage Notes

- **Metric Exposure**: All metrics are available at the configured HTTP endpoint in Prometheus format
- **Collection Strategy**: Periodic collectors run on configurable timers; HTTP metrics are captured in real-time via OpenTelemetry instrumentation
- **Prometheus Integration**: Metrics can be scraped by Prometheus or any compatible monitoring system
- **Labels**: Use labels like `organization_id`, `fleet`, and `status` to filter and group metrics for dashboards and alerts
- **Performance**: Adjust `tickerInterval` based on your monitoring needs to balance freshness with system load
