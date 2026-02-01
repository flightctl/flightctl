// Package infra provides testcontainers-based infrastructure for E2E tests.
// It implements a Hybrid Lifecycle strategy:
// - CI/CD: Per Suite lifecycle with fresh containers per package for isolation
// - Local Dev: Global lifecycle with container reuse for speed
package infra

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
)

var (
	once  sync.Once
	infra *SatelliteServices
)

// SatelliteServices holds the testcontainers for E2E test infrastructure.
type SatelliteServices struct {
	// Registry endpoints
	RegistryURL  string
	RegistryHost string
	RegistryPort string

	// Git server endpoints (external access from test harness)
	GitServerURL  string
	GitServerHost string
	GitServerPort int

	// Git server internal endpoints (for container-to-container communication)
	// Used by services running in kind cluster to access the git server
	GitServerInternalHost string
	GitServerInternalPort int

	// Prometheus endpoints
	PrometheusURL  string
	PrometheusHost string
	PrometheusPort string

	// Internal container references
	registry   testcontainers.Container
	gitServer  testcontainers.Container
	prometheus testcontainers.Container

	// Configuration
	network string
	reuse   bool
}

// GetInfra returns the satellite services, starting them if needed.
// Uses container reuse for local dev speed, fresh containers in CI.
// This is thread-safe and idempotent via sync.Once.
func GetInfra(ctx context.Context) *SatelliteServices {
	once.Do(func() {
		// Configure Docker host before any testcontainers operations
		ConfigureDockerHost()

		infra = &SatelliteServices{
			reuse:   !IsCI(),
			network: GetDockerNetwork(),
		}
		if err := infra.start(ctx); err != nil {
			logrus.Fatalf("failed to start satellite services: %v", err)
		}
	})
	return infra
}

// start initializes all satellite service containers.
func (s *SatelliteServices) start(ctx context.Context) error {
	logrus.Infof("Starting satellite services (reuse=%v, network=%s)", s.reuse, s.network)

	// Start Registry
	if err := s.startRegistry(ctx); err != nil {
		return fmt.Errorf("failed to start registry: %w", err)
	}

	// Upload images to registry (moved from Makefile push-e2e-agent-images)
	if err := s.UploadImages(); err != nil {
		return fmt.Errorf("failed to upload images: %w", err)
	}

	// Start Git Server
	if err := s.startGitServer(ctx); err != nil {
		return fmt.Errorf("failed to start git server: %w", err)
	}

	// Start Prometheus (always reuses for metric accumulation)
	if err := s.startPrometheus(ctx); err != nil {
		return fmt.Errorf("failed to start prometheus: %w", err)
	}

	logrus.Infof("Satellite services started successfully:")
	logrus.Infof("  Registry:   %s", s.RegistryURL)
	logrus.Infof("  Git Server: %s", s.GitServerURL)
	logrus.Infof("  Prometheus: %s", s.PrometheusURL)

	return nil
}

// Cleanup terminates containers. In CI, containers are terminated; in local dev, they stay running.
// Uses the global container registry for comprehensive cleanup (important when Ryuk is disabled).
func (s *SatelliteServices) Cleanup(ctx context.Context) {
	if !IsCI() {
		logrus.Info("Local dev mode: leaving satellite containers running for reuse")
		return
	}

	logrus.Info("CI mode: terminating all registered containers")
	// Use the global shutdown to ensure all containers are cleaned up
	// This is essential when Ryuk is disabled to prevent zombie containers
	ShutdownAll(ctx)
}

// SetEnvVars exports the service endpoints as environment variables for harness compatibility.
func (s *SatelliteServices) SetEnvVars() {
	os.Setenv("REGISTRY_ENDPOINT", s.RegistryURL)
	os.Setenv("E2E_GIT_SERVER_HOST", s.GitServerHost)
	os.Setenv("E2E_GIT_SERVER_PORT", fmt.Sprintf("%d", s.GitServerPort))
	os.Setenv("PROMETHEUS_ENDPOINT", s.PrometheusURL)
}

// IsCI detects if running in a CI environment.
func IsCI() bool {
	return os.Getenv("CI") != "" ||
		os.Getenv("GITHUB_ACTIONS") != "" ||
		os.Getenv("JENKINS_URL") != ""
}
