# Flight Control Metrics Architecture

This document describes the architecture and implementation of the metrics system in Flight Control. For user-facing configuration and usage, see [Metrics Configuration](../../user/metrics.md).

## Overview

Flight Control implements a modular metrics collection system using Prometheus-compatible metrics. The system is designed to be:
- **Configurable**: Each collector can be enabled/disabled independently
- **Extensible**: New collectors can be easily added
- **Performant**: Minimal impact on system performance
- **Observable**: Self-monitoring with metrics about metrics collection

## Architecture Components

### Core Metrics Infrastructure

The metrics system is built around several key components:

#### Metrics Server (`internal/instrumentation/instrumentation.go`)

The metrics server is responsible for:
- Exposing the `/metrics` HTTP endpoint
- Managing the lifecycle of metric collectors
- Providing OpenTelemetry integration
- Handling graceful shutdown

```go
type MetricsServer struct {
    log        *log.Logger
    cfg        *config.Config
    collectors []metrics.NamedCollector
}
```

#### Named Collector Interface (`internal/instrumentation/metrics/metrics.go`)

All collectors implement the `NamedCollector` interface:

```go
type NamedCollector interface {
    prometheus.Collector
    MetricsName() string
}
```

This interface extends Prometheus's `Collector` interface with a consistent naming mechanism for tracing and debugging.

#### Context-Aware Collectors

Collectors can optionally implement `ContextAwareCollector` for context injection:

```go
type ContextAwareCollector interface {
    prometheus.Collector
    WithContext(ctx context.Context) NamedCollector
}
```

### Collector Types

#### 1. System Collector (`internal/instrumentation/metrics/system.go`)

**Purpose**: Collects system-level metrics (CPU, memory, disk, network)

**Implementation**:
- Uses periodic collection with configurable intervals
- Implements `periodicCollectorConfig` for timing control
- Collects host-level metrics using system calls

**Configuration**:
```go
type systemCollectorConfig struct {
    periodicCollectorConfig
}
```

#### 2. HTTP Collector (`internal/instrumentation/metrics/http.go`)

**Purpose**: Collects HTTP request metrics and SLO compliance

**Implementation**:
- Implements HTTP middleware for request tracking
- Uses histograms for latency distribution
- Tracks SLO violations and error rates

**Configuration**:
```go
type httpCollectorConfig struct {
    collectorConfig
    SloMax         float64   `json:"sloMax,omitempty"`
    ApiLatencyBins []float64 `json:"apiLatencyBins,omitempty"`
}
```

#### 3. Business Metrics Collectors (`internal/instrumentation/metrics/business/`)

**Purpose**: Collect domain-specific metrics from Flight Control resources

**Implementation**:
- Located in `internal/instrumentation/metrics/business/`
- Each collector focuses on a specific resource type
- Uses database queries to gather metrics

**Available Collectors**:
- `DeviceCollector`: Device status and health metrics
- `FleetCollector`: Fleet management metrics
- `RepositoryCollector`: Repository status metrics
- `ResourceSyncCollector`: Resource synchronization metrics

### Configuration Architecture

#### Configuration Structure (`internal/config/config.go`)

The metrics configuration is defined in the main configuration structure:

```go
type metricsConfig struct {
    Enabled               bool                         `json:"enabled,omitempty"`
    Address               string                       `json:"address,omitempty"`
    SystemCollector       *systemCollectorConfig       `json:"systemCollector,omitempty"`
    HttpCollector         *httpCollectorConfig         `json:"httpCollector,omitempty"`
    DeviceCollector       *deviceCollectorConfig       `json:"deviceCollector,omitempty"`
    FleetCollector        *fleetCollectorConfig        `json:"fleetCollector,omitempty"`
    RepositoryCollector   *repositoryCollectorConfig   `json:"repositoryCollector,omitempty"`
    ResourceSyncCollector *resourceSyncCollectorConfig `json:"resourceSyncCollector,omitempty"`
}
```

#### Base Configuration Types

```go
type collectorConfig struct {
    Enabled bool `json:"enabled,omitempty"`
}

type periodicCollectorConfig struct {
    Enabled        bool          `json:"enabled,omitempty"`
    TickerInterval time.Duration `json:"tickerInterval,omitempty"`
}
```

### Metrics Collection Lifecycle

#### 1. Initialization

Metrics collectors are initialized in `cmd/flightctl-api/main.go`:

```go
if cfg.Metrics != nil && cfg.Metrics.Enabled {
    var collectors []metrics.NamedCollector
    
    if cfg.Metrics.DeviceCollector != nil && cfg.Metrics.DeviceCollector.Enabled {
        collectors = append(collectors, business.NewDeviceCollector(ctx, store, log, cfg))
    }
    // ... other collectors
    
    metricsServer := instrumentation.NewMetricsServer(log, cfg, collectors...)
    if err := metricsServer.Run(ctx); err != nil {
        log.Fatalf("Error running server: %s", err)
    }
}
```

#### 2. Collection Process

1. **Periodic Collection**: Collectors with `tickerInterval` run on a timer
2. **On-Demand Collection**: HTTP collector responds to request events
3. **Database Queries**: Business collectors query the database for current state
4. **Metric Updates**: Prometheus metrics are updated with new values

#### 3. Exposure

- Metrics are exposed via HTTP at the configured address
- OpenTelemetry integration provides additional observability
- Metrics can be scraped by Prometheus or other monitoring systems

### Tracing Integration

The metrics system integrates with OpenTelemetry tracing:

```go
type tracedCollector struct {
    ctx         context.Context
    collector   NamedCollector
    metricNames []string
}
```

Each metric collection operation is wrapped with tracing spans for observability.

### Business Metrics Implementation

#### Device Collector Architecture

The `DeviceCollector` demonstrates the typical business metrics pattern:

```go
type DeviceCollector struct {
    store *store.Store
    log   *log.Logger
    cfg   *config.Config
    
    // Prometheus metrics
    summaryMetrics   *prometheus.GaugeVec
    applicationMetrics *prometheus.GaugeVec
    updateMetrics    *prometheus.GaugeVec
}
```

**Collection Process**:
1. Query database for device status
2. Group devices by status and fleet
3. Update Prometheus gauges with current counts
4. Handle errors gracefully

#### Metric Naming Convention

Business metrics follow a consistent naming pattern:
- Prefix: `flightctl_`
- Resource: `devices_`, `fleets_`, `repositories_`
- Type: `summary_total`, `application_total`, `update_total`
- Example: `flightctl_devices_summary_total`

### Error Handling

#### Collector Error Handling

Collectors implement error handling patterns:

```go
func (c *DeviceCollector) Collect(ch chan<- prometheus.Metric) {
    defer func() {
        if r := recover(); r != nil {
            c.log.Errorf("Panic in DeviceCollector.Collect: %v", r)
        }
    }()
    
    // Collection logic with error handling
    if err := c.collectMetrics(ch); err != nil {
        c.log.Errorf("Error collecting device metrics: %v", err)
    }
}
```
