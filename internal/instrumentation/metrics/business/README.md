# Business Metrics Collectors

This directory contains business metrics collectors that follow the same pattern as the system metrics collector in `../system.go`. These collectors gather business-specific metrics from the Flight Control system.

## Available Collectors

### DeviceCollector

The `DeviceCollector` gathers device-related business metrics:

- `flightctl_devices_total`: Total number of devices managed
- `flightctl_devices_online_total`: Number of currently online/connected devices  
- `flightctl_devices_offline_total`: Number of currently offline devices

#### Usage

```go
import (
    "context"
    "github.com/flightctl/flightctl/internal/instrumentation/metrics/business"
    "github.com/flightctl/flightctl/internal/store"
    "github.com/sirupsen/logrus"
)

// Create the collector
ctx := context.Background()
store := // your store instance
log := logrus.New()
deviceCollector := business.NewDeviceCollector(ctx, store, log)

// Use with the metrics handler
handler := metrics.NewHandler(deviceCollector)
```

#### How it works

The collector:
1. Samples device metrics every 30 seconds
2. Uses the store's `Count()` method to get total device count
3. Uses the store's `Summary()` method to efficiently count online devices
4. Calculates offline devices as `total - online`
5. Updates Prometheus gauges with the current values

#### Device Status Logic

A device is considered **online** if its summary status is not "Unknown". The "Unknown" status indicates that the device is disconnected (hasn't reported in within the disconnection timeout).

### FleetCollector

The `FleetCollector` gathers fleet-related business metrics:

- `flightctl_fleets_total`: Total number of fleets managed
- `flightctl_fleet_rollout_status`: Status of ongoing fleet rollouts

#### How it works

The collector:
1. Samples fleet metrics every 30 seconds
2. Uses the store's `CountByRolloutStatus()` method to get fleet counts grouped by organization, version, and rollout status
3. Updates Prometheus gauges with the current values

### RepositoryCollector

The `RepositoryCollector` gathers repository-related business metrics:

- `flightctl_repositories_total`: Total number of repositories managed, grouped by organization and version

#### Usage

```go
import (
    "context"
    "github.com/flightctl/flightctl/internal/instrumentation/metrics/business"
    "github.com/flightctl/flightctl/internal/store"
    "github.com/sirupsen/logrus"
)

// Create the collector
ctx := context.Background()
store := // your store instance
log := logrus.New()
repositoryCollector := business.NewRepositoryCollector(ctx, store, log)

// Use with the metrics handler
handler := metrics.NewHandler(repositoryCollector)
```

#### Repository Version Logic

Repositories are grouped by their `spec.revision` field. If no revision is specified, they are grouped under the "unknown" version label.

### ResourceSyncCollector

The `ResourceSyncCollector` gathers resourcesync-related business metrics:

- `flightctl_resourcesyncs_total`: Total number of resource syncs managed, with status labels

#### Usage

```go
import (
    "context"
    "github.com/flightctl/flightctl/internal/instrumentation/metrics/business"
    "github.com/flightctl/flightctl/internal/store"
    "github.com/sirupsen/logrus"
)

// Create the collector
ctx := context.Background()
store := // your store instance
log := logrus.New()
resourceSyncCollector := business.NewResourceSyncCollector(ctx, store, log)

// Use with the metrics handler
handler := metrics.NewHandler(resourceSyncCollector)
```

## Adding New Collectors

To add a new business metrics collector:

1. Create a new file in this directory (e.g., `fleet.go`)
2. Implement the `metrics.NamedCollector` interface
3. Follow the same pattern as `DeviceCollector`:
   - Use Prometheus gauges/counters/histograms as appropriate
   - Implement background sampling with a ticker
   - Use proper locking for thread safety
   - Add comprehensive tests

Example structure:

```go
type MyCollector struct {
    myGauge prometheus.Gauge
    store   store.Store
    log     logrus.FieldLogger
    mu      sync.RWMutex
    ctx     context.Context
}

func NewMyCollector(ctx context.Context, store store.Store, log logrus.FieldLogger) *MyCollector {
    // Initialize collector
    // Start background sampling
    return collector
}

func (c *MyCollector) MetricsName() string {
    return "my_metric"
}

func (c *MyCollector) Describe(ch chan<- *prometheus.Desc) {
    // Describe metrics
}

func (c *MyCollector) Collect(ch chan<- prometheus.Metric) {
    // Collect metrics
}
```

## Testing

Each collector should include comprehensive tests that:
- Verify the collector implements required interfaces
- Test metric collection and description
- Mock the store to test business logic
- Verify correct metric values are set

Run tests with:
```bash
go test -v ./internal/instrumentation/metrics/business/
``` 