// Package satellite provides shared testcontainer-based services for E2E tests.
// These run the same regardless of deployment (K8s or Quadlet): registry, git server, prometheus.
// For deployment-specific infrastructure (where Flight Control runs), see the parent infra package
// and infra/k8s, infra/quadlet.
package satellite

import (
	"context"
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
)

var (
	once sync.Once
	svcs *Services
)

// Services holds the testcontainers for E2E test infrastructure (registry, git, prometheus).
// Same for all deployment types; created once and reused.
type Services struct {
	RegistryURL  string
	RegistryHost string
	RegistryPort string

	GitServerURL  string
	GitServerHost string
	GitServerPort int

	GitServerInternalHost string
	GitServerInternalPort int

	// gitServerPrivateKeyPath is set once when the git server starts (key copied from container to a temp file).
	gitServerPrivateKeyPath string

	PrometheusURL  string
	PrometheusHost string
	PrometheusPort string

	registry   testcontainers.Container
	gitServer  testcontainers.Container
	prometheus testcontainers.Container

	network        string
	reuse          bool
	registryReused bool // true when registry container was already running (reuse=true and container existed)
}

// Get returns the satellite services, starting them if needed.
func Get(ctx context.Context) *Services {
	once.Do(func() {
		ConfigureDockerHost()
		svcs = &Services{
			reuse:   true,
			network: GetDockerNetwork(),
		}
		if err := svcs.start(ctx); err != nil {
			logrus.Fatalf("failed to start satellite services: %v", err)
		}
	})
	return svcs
}

func (s *Services) start(ctx context.Context) error {
	logrus.Infof("Starting satellite services (reuse=%v, network=%s)", s.reuse, s.network)

	if err := s.startRegistry(ctx); err != nil {
		return fmt.Errorf("failed to start registry: %w", err)
	}
	if !s.registryReused {
		if err := s.UploadImages(); err != nil {
			return fmt.Errorf("failed to upload images: %w", err)
		}
	} else {
		logrus.Info("Skipping image bundle upload (registry container was reused)")
	}
	if err := s.startGitServer(ctx); err != nil {
		return fmt.Errorf("failed to start git server: %w", err)
	}
	if err := s.startPrometheus(ctx); err != nil {
		return fmt.Errorf("failed to start prometheus: %w", err)
	}

	logrus.Infof("Satellite services started: Registry=%s Git=%s Prometheus=%s",
		s.RegistryURL, s.GitServerURL, s.PrometheusURL)
	return nil
}

// Cleanup terminates containers when not reusing; with reuse=true containers stay running.
func (s *Services) Cleanup(ctx context.Context) {
	if s.reuse {
		logrus.Info("Satellite reuse enabled: leaving containers running")
		return
	}
	logrus.Info("Terminating satellite containers")
	ShutdownAll(ctx)
}
