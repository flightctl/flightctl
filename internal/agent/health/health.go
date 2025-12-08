// Package health provides health check functionality for the flightctl-agent.
// Used by greenboot to verify agent health during boot.
package health

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	serviceName = "flightctl-agent.service"
)

// Checker performs health checks on the agent.
type Checker struct {
	log       *log.PrefixLogger
	systemd   *client.Systemd
	timeout   time.Duration
	serverURL string
	verbose   bool
	output    io.Writer
}

// Option is a functional option for configuring the Checker.
type Option func(*Checker)

// WithTimeout sets the timeout for health checks.
func WithTimeout(t time.Duration) Option {
	return func(c *Checker) {
		c.timeout = t
	}
}

// WithServerURL sets the management server URL for connectivity check.
func WithServerURL(url string) Option {
	return func(c *Checker) {
		c.serverURL = url
	}
}

// WithVerbose enables verbose output.
func WithVerbose(v bool) Option {
	return func(c *Checker) {
		c.verbose = v
	}
}

// WithOutput sets the output writer for messages.
func WithOutput(w io.Writer) Option {
	return func(c *Checker) {
		c.output = w
	}
}

// WithSystemdClient sets a custom systemd client (for testing).
func WithSystemdClient(systemd *client.Systemd) Option {
	return func(c *Checker) {
		c.systemd = systemd
	}
}

// New creates a new health checker with the given options.
func New(log *log.PrefixLogger, opts ...Option) *Checker {
	c := &Checker{
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
func (c *Checker) Run(ctx context.Context) error {
	if err := c.checkServiceStatus(ctx); err != nil {
		c.printError("Service check failed: %v", err)
		return err
	}

	// Check connectivity (warn only, never fail)
	c.checkConnectivity(ctx)

	c.printInfo("All health checks passed")
	return nil
}

// checkServiceStatus verifies the flightctl-agent service is enabled and active.
func (c *Checker) checkServiceStatus(ctx context.Context) error {
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
	return nil
}

// checkConnectivity tries to reach the management server.
// This is a warning-only check - it never causes the health check to fail.
func (c *Checker) checkConnectivity(ctx context.Context) {
	if c.serverURL == "" {
		c.printInfo("Connectivity check skipped (no server URL configured)")
		return
	}

	c.printInfo("Checking connectivity to %s...", c.serverURL)

	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.serverURL, nil)
	if err != nil {
		c.printWarn("Cannot reach server: %v", err)
		return
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		c.printWarn("Cannot reach server: %v", err)
		return
	}
	defer resp.Body.Close()

	c.printInfo("Server reachable (status: %d)", resp.StatusCode)
}

func (c *Checker) printInfo(format string, args ...any) {
	if c.output != nil && c.verbose {
		fmt.Fprintf(c.output, "[health] "+format+"\n", args...)
	}
}

func (c *Checker) printWarn(format string, args ...any) {
	if c.output != nil {
		fmt.Fprintf(c.output, "[health] WARNING: "+format+"\n", args...)
	}
}

func (c *Checker) printError(format string, args ...any) {
	if c.output != nil {
		fmt.Fprintf(c.output, "[health] ERROR: "+format+"\n", args...)
	}
}
