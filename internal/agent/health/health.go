// Copyright 2025 Red Hat, Inc.
// SPDX-License-Identifier: Apache-2.0

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

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	serviceName = "flightctl-agent.service"
)

// SystemdClient defines the interface for systemd operations.
// This interface allows mocking D-Bus interactions in tests.
type SystemdClient interface {
	Close()
	IsServiceEnabled(ctx context.Context, service string) (bool, error)
	IsServiceActive(ctx context.Context, service string) (bool, error)
}

// SystemdClientFactory creates SystemdClient instances.
type SystemdClientFactory func(ctx context.Context) (SystemdClient, error)

// systemdClient wraps a D-Bus connection for systemd operations.
type systemdClient struct {
	conn *dbus.Conn
}

// NewSystemdClient creates a new systemd client connected via D-Bus.
func NewSystemdClient(ctx context.Context) (SystemdClient, error) {
	conn, err := dbus.NewWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting to D-Bus: %w", err)
	}
	return &systemdClient{conn: conn}, nil
}

// Close closes the D-Bus connection.
func (s *systemdClient) Close() {
	s.conn.Close()
}

// IsServiceEnabled checks if a systemd service is enabled.
func (s *systemdClient) IsServiceEnabled(ctx context.Context, service string) (bool, error) {
	props, err := s.conn.GetAllPropertiesContext(ctx, service)
	if err != nil {
		return false, fmt.Errorf("getting service properties: %w", err)
	}

	state, ok := props["UnitFileState"]
	if !ok {
		return false, fmt.Errorf("could not find UnitFileState property")
	}

	return state == "enabled", nil
}

// IsServiceActive checks if a systemd service is active and not failed.
func (s *systemdClient) IsServiceActive(ctx context.Context, service string) (bool, error) {
	props, err := s.conn.GetAllPropertiesContext(ctx, service)
	if err != nil {
		return false, fmt.Errorf("getting service properties: %w", err)
	}

	activeState, ok := props["ActiveState"]
	if !ok {
		return false, fmt.Errorf("could not find ActiveState property")
	}

	if activeState == "failed" {
		return false, fmt.Errorf("service %s has failed", service)
	}

	// https://github.com/systemd/systemd/blob/main/src/systemctl/systemctl-is-active.c
	return activeState == "active" || activeState == "reloading", nil
}

// Checker performs health checks on the agent.
type Checker struct {
	log            *log.PrefixLogger
	timeout        time.Duration
	serverURL      string
	verbose        bool
	output         io.Writer
	greenbootMode  bool
	systemdFactory SystemdClientFactory
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

// WithGreenbootMode enables greenboot integration mode.
func WithGreenbootMode(enabled bool) Option {
	return func(c *Checker) {
		c.greenbootMode = enabled
	}
}

// WithSystemdFactory sets the factory for creating systemd clients.
// Used for injecting mock clients in tests.
func WithSystemdFactory(factory SystemdClientFactory) Option {
	return func(c *Checker) {
		c.systemdFactory = factory
	}
}

// New creates a new health checker with the given options.
func New(log *log.PrefixLogger, opts ...Option) *Checker {
	c := &Checker{
		log:            log,
		timeout:        30 * time.Second,
		output:         os.Stdout,
		systemdFactory: NewSystemdClient, // Default to real D-Bus client
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Run executes health checks and returns error if critical checks fail.
func (c *Checker) Run(ctx context.Context) error {
	// Check service status via D-Bus
	if err := c.checkServiceStatus(ctx); err != nil {
		return err
	}

	// Check connectivity (warn only, never fail)
	c.checkConnectivity(ctx)

	c.printInfo("All health checks passed")
	return nil
}

// checkServiceStatus verifies the flightctl-agent service is enabled and active.
func (c *Checker) checkServiceStatus(ctx context.Context) error {
	c.printInfo("Checking %s status via D-Bus...", serviceName)

	systemd, err := c.systemdFactory(ctx)
	if err != nil {
		return err
	}
	defer systemd.Close()

	// Check if service is enabled
	enabled, err := systemd.IsServiceEnabled(ctx, serviceName)
	if err != nil {
		return err
	}
	if !enabled {
		c.printInfo("Service is not enabled, skipping active check")
		// Not enabled = nothing to check, exit successfully
		return nil
	}
	c.printInfo("Service is enabled")

	// Check if service is active (not failed)
	active, err := systemd.IsServiceActive(ctx, serviceName)
	if err != nil {
		return err
	}
	if !active {
		return fmt.Errorf("service is not active")
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

	// Create HTTP client with timeout and skip TLS verification for health check
	client := &http.Client{
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

	resp, err := client.Do(req)
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
