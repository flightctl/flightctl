// Package health provides health check functionality for the flightctl-agent.
// Used by greenboot to verify agent health during boot.
package health

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	serviceName = "flightctl-agent.service"

	// defaultStabilityWindow is how long the service must remain active
	// to be considered healthy.
	defaultStabilityWindow = 60 * time.Second

	// pollInterval is how often we check the service status.
	pollInterval = 5 * time.Second
)

// checker performs health checks on the agent.
type checker struct {
	log             *log.PrefixLogger
	systemd         *client.Systemd
	timeout         time.Duration
	stabilityWindow time.Duration
	verbose         bool
	output          io.Writer
}

// Option is a functional option for configuring the checker.
type Option func(*checker)

// WithTimeout sets the timeout for waiting for the service to become active.
func WithTimeout(t time.Duration) Option {
	return func(c *checker) {
		c.timeout = t
	}
}

// WithStabilityWindow sets the duration the service must remain stable.
func WithStabilityWindow(d time.Duration) Option {
	return func(c *checker) {
		c.stabilityWindow = d
	}
}

// WithVerbose enables verbose output.
func WithVerbose(v bool) Option {
	return func(c *checker) {
		c.verbose = v
	}
}

// WithOutput sets the output writer for messages.
func WithOutput(w io.Writer) Option {
	return func(c *checker) {
		c.output = w
	}
}

// WithSystemdClient sets a custom systemd client (for testing).
func WithSystemdClient(systemd *client.Systemd) Option {
	return func(c *checker) {
		c.systemd = systemd
	}
}

// NewChecker creates a new health checker with the given options.
func NewChecker(log *log.PrefixLogger, opts ...Option) *checker {
	c := &checker{
		log:             log,
		timeout:         150 * time.Second,
		stabilityWindow: defaultStabilityWindow,
		output:          os.Stdout,
	}
	for _, opt := range opts {
		opt(c)
	}

	// Configure logger output and level
	c.log.SetOutput(c.output)
	if c.verbose {
		c.log.Level("debug")
	} else {
		c.log.Level("warn") // Only show warnings and errors when not verbose
	}

	// Default systemd client if not provided
	if c.systemd == nil {
		c.systemd = client.NewSystemd(executer.NewCommonExecuter(), v1beta1.RootUsername)
	}
	return c
}

// Run executes health checks and returns error if critical checks fail.
// It polls until the service becomes active (up to timeout), then monitors
// for a stability window to ensure the service doesn't crash-loop.
//
// Phase 1: Wait up to `timeout` for service to become active.
// Phase 2: Monitor for `stabilityWindow` to ensure service stays active.
// Total maximum time: timeout + stabilityWindow.
func (c *checker) Run(ctx context.Context) error {
	// Phase 1: Wait for service to become active (up to timeout)
	phase1Ctx, cancel1 := context.WithTimeout(ctx, c.timeout)
	defer cancel1()

	if err := c.waitForServiceActive(phase1Ctx); err != nil {
		c.printError("Service check failed: %v", err)
		return err
	}

	// Phase 2: Monitor stability window (separate timeout)
	phase2Ctx, cancel2 := context.WithTimeout(ctx, c.stabilityWindow+pollInterval)
	defer cancel2()

	if err := c.monitorStability(phase2Ctx); err != nil {
		c.printError("Stability check failed: %v", err)
		return err
	}

	c.printInfo("All health checks passed")
	return nil
}

// waitForServiceActive polls until the service becomes active or context expires.
// Returns an error if the service is disabled, failed, or doesn't become active in time.
func (c *checker) waitForServiceActive(ctx context.Context) error {
	c.printInfo("Waiting for %s to become active (timeout: %v)...", serviceName, c.timeout)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Check immediately, then poll
	for {
		props, err := c.getServiceProps(ctx)
		if err != nil {
			c.printInfo("Error getting service status: %v, retrying...", err)
		} else {
			// Check if service is enabled - fail if not
			unitFileState := props["UnitFileState"]
			if unitFileState != "enabled" {
				return fmt.Errorf("service is not enabled (state: %s)", unitFileState)
			}

			activeState := props["ActiveState"]
			if activeState == "failed" {
				return fmt.Errorf("service has failed")
			}
			if activeState == "active" || activeState == "reloading" {
				c.printInfo("Service is active")
				return nil
			}
			c.printInfo("Service state: %s, waiting...", activeState)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for service to become active")
		case <-ticker.C:
			// Continue polling
		}
	}
}

// monitorStability watches the service for a stability window to ensure
// it stays active. If the service becomes inactive or fails during this window,
// the health check fails.
func (c *checker) monitorStability(ctx context.Context) error {
	c.printInfo("Monitoring service stability for %v...", c.stabilityWindow)

	stabilityDeadline := time.Now().Add(c.stabilityWindow)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout during stability monitoring")
		case <-ticker.C:
			props, err := c.getServiceProps(ctx)
			if err != nil {
				c.printInfo("Error getting service status during stability check: %v", err)
				continue
			}

			// Check service is still active
			activeState := props["ActiveState"]
			if activeState == "failed" {
				return fmt.Errorf("service failed during stability window")
			}
			if activeState != "active" && activeState != "reloading" {
				return fmt.Errorf("service became inactive during stability window (state: %s)", activeState)
			}

			// Check if stability window has passed
			if time.Now().After(stabilityDeadline) {
				c.printInfo("Service remained stable for %v", c.stabilityWindow)
				// Report final connectivity status
				c.reportConnectivityStatus(props)
				return nil
			}

			remaining := time.Until(stabilityDeadline).Round(time.Second)
			c.printInfo("Service stable, %v remaining...", remaining)
		}
	}
}

// getServiceProps retrieves systemd properties for the service.
func (c *checker) getServiceProps(ctx context.Context) (map[string]string, error) {
	units, err := c.systemd.ShowByMatchPattern(ctx, []string{serviceName})
	if err != nil {
		return nil, fmt.Errorf("getting service status: %w", err)
	}
	if len(units) == 0 {
		return nil, fmt.Errorf("service %s not found", serviceName)
	}
	return units[0], nil
}

// reportConnectivityStatus reads and logs the agent's self-reported connectivity status.
// The agent reports its status via sd_notify(STATUS=...), which is visible in StatusText.
// This is informational only and does not affect the health check result.
func (c *checker) reportConnectivityStatus(props map[string]string) {
	statusText, ok := props["StatusText"]
	if !ok || statusText == "" {
		c.printInfo("Connectivity: unknown (agent has not reported status)")
		return
	}
	c.printInfo("Agent status: %s", statusText)
}

func (c *checker) printInfo(format string, args ...any) {
	c.log.Debugf(format, args...)
}

func (c *checker) printError(format string, args ...any) {
	c.log.Errorf(format, args...)
}
