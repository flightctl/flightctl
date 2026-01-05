package common

import (
	"time"

	"github.com/flightctl/flightctl/internal/util"
)

// MetricsConfig holds metrics collection configuration.
type MetricsConfig struct {
	Enabled               bool                         `json:"enabled,omitempty"`
	Address               string                       `json:"address,omitempty"`
	SystemCollector       *SystemCollectorConfig       `json:"systemCollector,omitempty"`
	HttpCollector         *HttpCollectorConfig         `json:"httpCollector,omitempty"`
	DeviceCollector       *DeviceCollectorConfig       `json:"deviceCollector,omitempty"`
	FleetCollector        *FleetCollectorConfig        `json:"fleetCollector,omitempty"`
	RepositoryCollector   *RepositoryCollectorConfig   `json:"repositoryCollector,omitempty"`
	ResourceSyncCollector *ResourceSyncCollectorConfig `json:"resourceSyncCollector,omitempty"`
	WorkerCollector       *WorkerCollectorConfig       `json:"workerCollector,omitempty"`
}

// CollectorConfig is the base configuration for simple collectors.
type CollectorConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// PeriodicCollectorConfig is the base configuration for periodic collectors.
type PeriodicCollectorConfig struct {
	Enabled        bool          `json:"enabled,omitempty"`
	TickerInterval util.Duration `json:"tickerInterval,omitempty"`
}

// SystemCollectorConfig holds system metrics collector configuration.
type SystemCollectorConfig struct {
	PeriodicCollectorConfig
}

// HttpCollectorConfig holds HTTP metrics collector configuration.
type HttpCollectorConfig struct {
	CollectorConfig
}

// DeviceCollectorConfig holds device metrics collector configuration.
type DeviceCollectorConfig struct {
	PeriodicCollectorConfig
	GroupByFleet bool `json:"groupByFleet,omitempty"`
}

// FleetCollectorConfig holds fleet metrics collector configuration.
type FleetCollectorConfig struct {
	PeriodicCollectorConfig
}

// RepositoryCollectorConfig holds repository metrics collector configuration.
type RepositoryCollectorConfig struct {
	PeriodicCollectorConfig
}

// ResourceSyncCollectorConfig holds resource sync metrics collector configuration.
type ResourceSyncCollectorConfig struct {
	PeriodicCollectorConfig
}

// WorkerCollectorConfig holds worker metrics collector configuration.
type WorkerCollectorConfig struct {
	CollectorConfig
}

// NewDefaultMetrics returns a default metrics configuration.
func NewDefaultMetrics() *MetricsConfig {
	return &MetricsConfig{
		Enabled: true,
		Address: ":15690",
		SystemCollector: &SystemCollectorConfig{
			PeriodicCollectorConfig: PeriodicCollectorConfig{
				Enabled:        true,
				TickerInterval: util.Duration(5 * time.Second),
			},
		},
		HttpCollector: &HttpCollectorConfig{
			CollectorConfig: CollectorConfig{
				Enabled: true,
			},
		},
		DeviceCollector: &DeviceCollectorConfig{
			PeriodicCollectorConfig: PeriodicCollectorConfig{
				Enabled:        true,
				TickerInterval: util.Duration(30 * time.Second),
			},
			GroupByFleet: true,
		},
		FleetCollector: &FleetCollectorConfig{
			PeriodicCollectorConfig: PeriodicCollectorConfig{
				Enabled:        true,
				TickerInterval: util.Duration(30 * time.Second),
			},
		},
		RepositoryCollector: &RepositoryCollectorConfig{
			PeriodicCollectorConfig: PeriodicCollectorConfig{
				Enabled:        true,
				TickerInterval: util.Duration(30 * time.Second),
			},
		},
		ResourceSyncCollector: &ResourceSyncCollectorConfig{
			PeriodicCollectorConfig: PeriodicCollectorConfig{
				Enabled:        true,
				TickerInterval: util.Duration(30 * time.Second),
			},
		},
	}
}
