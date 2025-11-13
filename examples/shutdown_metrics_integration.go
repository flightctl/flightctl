package main

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/instrumentation/metrics"
	shutdownmetrics "github.com/flightctl/flightctl/internal/instrumentation/metrics/shutdown"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/sirupsen/logrus"
)

// ExampleServiceIntegration demonstrates how to integrate shutdown metrics
// into a FlightCtl service, addressing the scraping timing concern
func ExampleServiceIntegration() {
	log := logrus.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Create shutdown metrics collector
	shutdownCollector := shutdownmetrics.NewShutdownCollector(ctx, log)

	// 2. Create shutdown manager and configure it with metrics
	shutdownManager := shutdown.NewShutdownManager(log)
	shutdownManager.SetServiceName("flightctl-api") // Set your service name
	shutdownManager.SetMetricsCollector(shutdownCollector)

	// Enable fail-fast behavior for rapid error propagation
	shutdownManager.EnableFailFast(cancel)

	// 3. Create metrics server with all collectors including shutdown metrics
	// This integrates with FlightCtl's existing metrics infrastructure
	metricsServer := metrics.NewMetricsServer(
		log,
		shutdownCollector, // Add shutdown collector to existing metrics
		// Add other existing collectors here (worker, system, etc.)
	)

	// 4. Register service components with shutdown manager
	// These should be ordered by shutdown priority (PriorityHighest = shutdown first)

	// HTTP server (highest priority - stop accepting new requests first)
	shutdownManager.Register("http-server", shutdown.PriorityHighest, shutdown.TimeoutStandard, func(ctx context.Context) error {
		log.Info("Shutting down HTTP server")
		// Your HTTP server shutdown logic here
		// The metrics server itself will continue running during its own
		// 5-second graceful shutdown window, allowing metrics to be scraped
		return nil
	})

	// Application-specific components
	shutdownManager.Register("cache", shutdown.PriorityLow, shutdown.TimeoutCache, func(ctx context.Context) error {
		log.Info("Shutting down cache")
		// Your cache shutdown logic here
		return nil
	})

	// Database connections (lower priority)
	shutdownManager.Register("database", shutdown.PriorityLowest, shutdown.TimeoutDatabase, func(ctx context.Context) error {
		log.Info("Closing database connections")
		// Record resource cleanup for observability
		if err := closeDatabaseConnections(); err != nil {
			shutdownCollector.RecordResourceCleanup("database", "failed")
			return err
		}
		shutdownCollector.RecordResourceCleanup("database", "success")
		return nil
	})

	// KV Store (lower priority)
	shutdownManager.Register("kvstore", shutdown.PriorityLowest, shutdown.TimeoutStandard, func(ctx context.Context) error {
		log.Info("Closing KV store connections")
		if err := closeKVStoreConnections(); err != nil {
			shutdownCollector.RecordResourceCleanup("kvstore", "failed")
			return err
		}
		shutdownCollector.RecordResourceCleanup("kvstore", "success")
		return nil
	})

	// 5. Start metrics server in background
	go func() {
		if err := metricsServer.Run(ctx); err != nil {
			log.WithError(err).Error("Metrics server failed")
		}
	}()

	// 6. Set up signal handling with shutdown manager
	// This automatically records signal metrics and coordinates shutdown
	shutdown.HandleSignalsWithManager(log, shutdownManager, shutdown.DefaultGracefulShutdownTimeout)

	log.Info("Service started with shutdown metrics integration")

	// Key benefits of this integration:
	// - Shutdown metrics are immediately available when shutdown starts
	// - Component-level timing and error tracking
	// - Signal handling metrics for operational visibility
	// - Resource cleanup verification
	// - Metrics server continues running during its own graceful shutdown,
	//   providing 5 seconds for scrapers to collect final shutdown metrics
	// - Fail-fast behavior with metrics tracking for rapid error detection

	// Your main service logic here...
	<-ctx.Done()
	log.Info("Service shutting down...")
}

// Helper functions (implement based on your service)
func closeDatabaseConnections() error {
	// Implement your database connection cleanup
	return nil
}

func closeKVStoreConnections() error {
	// Implement your KV store connection cleanup
	return nil
}

// ExampleFailFastScenario demonstrates fail-fast behavior with metrics
func ExampleFailFastScenario(shutdownManager *shutdown.ShutdownManager, shutdownCollector *shutdownmetrics.ShutdownCollector) {
	// Simulate a critical component failure
	componentName := "critical-service"
	err := fmt.Errorf("database connection pool exhausted")

	// This will:
	// 1. Record the fail-fast event in metrics
	// 2. Trigger immediate shutdown if fail-fast is enabled
	// 3. Allow metrics scraper to collect the failure data
	shutdownManager.TriggerFailFast(componentName, err)

	// The fail-fast metrics will be available immediately for alerting:
	// flightctl_shutdown_fail_fast_events_total{component="critical-service",reason="database connection pool exhausted"}
}

// ExampleMetricsQueries shows example Prometheus queries for shutdown observability
/*
Example Prometheus queries for shutdown observability:

1. Shutdown Success Rate:
   rate(flightctl_shutdown_completed_total{outcome="success"}[5m]) /
   rate(flightctl_shutdown_completed_total[5m])

2. Component Timeout Rate:
   rate(flightctl_shutdown_component_status_total{status="timeout"}[5m])

3. Average Shutdown Duration:
   histogram_quantile(0.95, flightctl_shutdown_total_duration_seconds_bucket)

4. Component Shutdown Duration by Priority:
   histogram_quantile(0.95,
     flightctl_shutdown_component_duration_seconds_bucket) by (priority)

5. Fail-Fast Events:
   increase(flightctl_shutdown_fail_fast_events_total[1h])

6. Signal Handling:
   increase(flightctl_shutdown_signals_received_total[1h]) by (signal)

7. Resource Cleanup Status:
   flightctl_shutdown_resource_cleanup_total by (resource_type, status)

8. Active Shutdowns:
   flightctl_shutdown_state > 0

These metrics enable:
- SLA monitoring for shutdown performance
- Early detection of problematic components
- Resource leak detection
- Operational dashboards for service health
- Automated alerting on shutdown failures

The metrics are designed to be scraped during the shutdown process,
with the metrics server's own graceful shutdown providing adequate
time for collection before final termination.
*/

func main() {
	// This is an example - in practice, you would integrate this
	// into your actual FlightCtl service
	fmt.Println("This is an example of shutdown metrics integration")
	fmt.Println("See ExampleServiceIntegration() for implementation details")
}
