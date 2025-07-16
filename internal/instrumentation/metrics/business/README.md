# Business Metrics Collectors

This directory contains business metrics collectors that gather business-specific metrics from the Flight Control system.

## Available Collectors

### DeviceCollector

The `DeviceCollector` gathers device-related business metrics from the `Device` model (`internal/store/model/device.go`).

#### Metrics

##### Summary Status Metrics
- **`flightctl_devices_summary_total`**: Total number of devices managed (by summary status)
  - **Labels**:
    - `organization_id`: From `Device.OrgID` (UUID field)
    - `version`: From `Device.Status.Data.Summary.Status` (DeviceSummaryStatusType enum)
    - `fleet`: From device's fleet association (if any)
    - `status`: From `Device.Status.Data.Summary.Status` (DeviceSummaryStatusType enum)
      - Values: `Online`, `Degraded`, `Error`, `Rebooting`, `PoweredOff`, `Unknown`

##### Application Status Metrics
- **`flightctl_devices_application_total`**: Total number of devices managed (by application status)
  - **Labels**:
    - `organization_id`: From `Device.OrgID` (UUID field)
    - `version`: From `Device.Status.Data.ApplicationsSummary.Status` (ApplicationsSummaryStatusType enum)
    - `fleet`: From device's fleet association (if any)
    - `status`: From `Device.Status.Data.ApplicationsSummary.Status` (ApplicationsSummaryStatusType enum)
      - Values: `Healthy`, `Degraded`, `Error`, `Unknown`

##### System Update Status Metrics
- **`flightctl_devices_update_total`**: Total number of devices managed (by system update status)
  - **Labels**:
    - `organization_id`: From `Device.OrgID` (UUID field)
    - `version`: From `Device.Status.Data.Updated.Status` (DeviceUpdatedStatusType enum)
    - `fleet`: From device's fleet association (if any)
    - `status`: From `Device.Status.Data.Updated.Status` (DeviceUpdatedStatusType enum)
      - Values: `UpToDate`, `OutOfDate`, `Updating`, `Unknown`

#### How it works

The collector:
1. Samples device metrics every 30 seconds
2. Uses the store's `CountByFleetAndStatus()` method to get device counts grouped by organization, version, fleet, and status
3. Updates Prometheus gauges with the current values
4. Provides methods for incrementing counters for lifecycle events

#### Device Status Logic

A device's status is determined by the following fields in `Device.Status.Data`:
- **Summary Status**: `Device.Status.Data.Summary.Status` - Overall device health
- **Application Status**: `Device.Status.Data.ApplicationsSummary.Status` - Health of applications running on the device
- **Update Status**: `Device.Status.Data.Updated.Status` - Status of system updates

### FleetCollector

The `FleetCollector` gathers fleet-related business metrics from the `Fleet` model (`internal/store/model/fleet.go`).

#### Metrics

- **`flightctl_fleets_total`**: Total number of fleets managed
  - **Labels**:
    - `organization_id`: From `Fleet.OrgID` (UUID field)
    - `version`: From fleet's template version or "unknown" if not specified

- **`flightctl_fleet_rollout_status`**: Status of ongoing fleet rollouts
  - **Labels**:
    - `organization_id`: From `Fleet.OrgID` (UUID field)
    - `version`: From fleet's template version or "unknown" if not specified
    - `status`: From `Fleet.Status.Data.Rollout` status or "none" if no rollout in progress

#### How it works

The collector:
1. Samples fleet metrics every 30 seconds
2. Uses the store's `CountByRolloutStatus()` method to get fleet counts grouped by organization, version, and rollout status
3. Updates Prometheus gauges with the current values

#### Fleet Status Logic

Fleet status is determined by:
- **Rollout Status**: `Fleet.Status.Data.Rollout` - Current rollout state
- **Conditions**: `Fleet.Status.Data.Conditions` - Array of condition objects indicating fleet health
- **Devices Summary**: `Fleet.Status.Data.DevicesSummary` - Summary of devices in the fleet

### RepositoryCollector

The `RepositoryCollector` gathers repository-related business metrics from the `Repository` model (`internal/store/model/repository.go`).

#### Metrics

- **`flightctl_repositories_total`**: Total number of repositories managed
  - **Labels**:
    - `organization_id`: From `Repository.OrgID` (UUID field)
    - `version`: From `Repository.Spec.Data.Revision` or "unknown" if not specified

#### How it works

The collector:
1. Samples repository metrics every 30 seconds
2. Uses the store's `CountByOrgAndVersion()` method to get repository counts grouped by organization and version
3. Updates Prometheus gauges with the current values

#### Repository Version Logic

Repositories are grouped by their `spec.revision` field. If no revision is specified, they are grouped under the "unknown" version label.

### ResourceSyncCollector

The `ResourceSyncCollector` gathers resourcesync-related business metrics from the `ResourceSync` model (`internal/store/model/resourcesync.go`).

#### Metrics

- **`flightctl_resourcesyncs_total`**: Total number of resource syncs managed
  - **Labels**:
    - `organization_id`: From `ResourceSync.OrgID` (UUID field)
    - `status`: From `ResourceSync.Status.Data.Conditions` status or "unknown" if no conditions
    - `version`: From `ResourceSync.Spec.Data.TargetRevision` or "unknown" if not specified

#### How it works

The collector:
1. Samples resource sync metrics every 30 seconds
2. Uses the store's `CountByOrgStatusAndVersion()` method to get resource sync counts grouped by organization, status, and version
3. Updates Prometheus gauges with the current values

#### ResourceSync Status Logic

ResourceSync status is determined by:
- **Conditions**: `ResourceSync.Status.Data.Conditions` - Array of condition objects indicating sync status
- **Observed Commit**: `ResourceSync.Status.Data.ObservedCommit` - Last commit hash that was synced
- **Observed Generation**: `ResourceSync.Status.Data.ObservedGeneration` - Last generation that was synced

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