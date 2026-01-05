// Package health provides health check functionality for the flightctl-agent.
// Used by greenboot to verify agent health during boot.
package health

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	serviceName = "flightctl-agent.service"
)

// checker performs health checks on the agent.
type checker struct {
	log     *log.PrefixLogger
	systemd *client.Systemd
	timeout time.Duration
	verbose bool
	output  io.Writer
}

// Option is a functional option for configuring the checker.
type Option func(*checker)

// WithTimeout sets the timeout for health checks.
func WithTimeout(t time.Duration) Option {
	return func(c *checker) {
		c.timeout = t
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
		log:     log,
		timeout: 30 * time.Second,
		output:  os.Stdout,
	}
	for _, opt := range opts {
		opt(c)
	}
	// Default systemd client if not provided
	if c.systemd == nil {
		c.systemd = client.NewSystemd(&executer.CommonExecuter{})
	}
	return c
}

// Run executes health checks and returns error if critical checks fail.
func (c *checker) Run(ctx context.Context) error {
	if err := c.checkServiceStatus(ctx); err != nil {
		c.printError("Service check failed: %v", err)
		return err
	}

	c.printInfo("All health checks passed")
	return nil
}

// checkServiceStatus verifies the flightctl-agent service is enabled and active.
// It also reports the agent's connectivity status (from StatusText) for informational purposes.
func (c *checker) checkServiceStatus(ctx context.Context) error {
	c.printInfo("Checking %s status...", serviceName)

	units, err := c.systemd.ShowByMatchPattern(ctx, []string{serviceName})
	if err != nil {
		return fmt.Errorf("getting service status: %w", err)
	}

	if len(units) == 0 {
		return fmt.Errorf("service %s not found", serviceName)
	}

	props := units[0]

	// Check if service is enabled
	unitFileState, ok := props["UnitFileState"]
	if !ok {
		return fmt.Errorf("could not find UnitFileState property")
	}
	if unitFileState != "enabled" {
		c.printInfo("Service is not enabled (state: %s), skipping active check", unitFileState)
		return nil
	}
	c.printInfo("Service is enabled")

	// Check if service is active
	activeState, ok := props["ActiveState"]
	if !ok {
		return fmt.Errorf("could not find ActiveState property")
	}

	if activeState == "failed" {
		return fmt.Errorf("service has failed")
	}

	if activeState != "active" && activeState != "reloading" {
		return fmt.Errorf("service is not active (state: %s)", activeState)
	}

	c.printInfo("Service is active")

	// Report connectivity status from agent's sd_notify(STATUS=...)
	// This is informational only - connectivity issues don't affect rollback decisions
	c.reportConnectivityStatus(props)

	return nil
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
	if c.output != nil && c.verbose {
		fmt.Fprintf(c.output, "[health] "+format+"\n", args...)
	}
}

func (c *checker) printError(format string, args ...any) {
	if c.output != nil {
		fmt.Fprintf(c.output, "[health] ERROR: "+format+"\n", args...)
	}
}
