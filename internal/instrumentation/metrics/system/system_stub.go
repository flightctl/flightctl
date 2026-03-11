//go:build !linux

package system

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/prometheus/client_golang/prometheus"
)

// NewSystemCollector returns nil on non-Linux so binaries that never import this package
// (e.g. flightctl-restore) are unaffected; those that do (flightctl-api, flightctl-worker)
// build on all platforms and get a real collector only on Linux.
func NewSystemCollector(_ context.Context, _ *config.Config) *SystemCollector {
	return nil
}

// SystemCollector stub type for !linux so *SystemCollector exists and satisfies prometheus.Collector.
type SystemCollector struct{}

func (c *SystemCollector) Describe(ch chan<- *prometheus.Desc) {}

func (c *SystemCollector) Collect(ch chan<- prometheus.Metric) {}

func (c *SystemCollector) Shutdown() error { return nil }
