// Package satellite provides shared testcontainer-based services for E2E tests.
// These run the same regardless of deployment (K8s or Quadlet): registry, git server, prometheus, jaeger.
// For deployment-specific infrastructure (where Flight Control runs), see the parent infra package
// and infra/k8s, infra/quadlet.
package satellite

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
)

var (
	once sync.Once
	svcs *Services
)

// Services holds the testcontainers for E2E test infrastructure (registry, git, prometheus, jaeger).
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

	JaegerURL          string
	JaegerHost         string
	JaegerPort         string
	JaegerOTLPEndpoint string

	registry   testcontainers.Container
	gitServer  testcontainers.Container
	prometheus testcontainers.Container
	jaeger     testcontainers.Container

	network        string
	reuse          bool
	registryReused bool // true when registry container was already running (reuse=true and container existed)
}

// Service identifies a satellite service that can be started individually.
type Service string

const (
	ServiceRegistry   Service = "registry"
	ServiceGitServer  Service = "git-server"
	ServicePrometheus Service = "prometheus"
	ServiceTracing    Service = "tracing"
)

// AllServices is the default set of shared satellite services (started by Get(ctx)).
// Does not include ServiceTracing; use infra.TracingProvider for opt-in tracing.
var AllServices = []Service{ServiceRegistry, ServiceGitServer, ServicePrometheus}

// Get returns the satellite services, starting all of them if needed (singleton).
func Get(ctx context.Context) *Services {
	once.Do(func() {
		ConfigureDockerHost()
		var err error
		svcs, err = StartServices(ctx, AllServices)
		if err != nil {
			logrus.Fatalf("failed to start satellite services: %v", err)
		}
	})
	return svcs
}

// StartServices starts only the requested satellite services with reuse=true.
// For registry, image bundles are uploaded when the container is freshly created (not reused).
func StartServices(ctx context.Context, services []Service) (*Services, error) {
	s := &Services{
		reuse:   true,
		network: GetDockerNetwork(),
	}
	for _, svc := range services {
		switch svc {
		case ServiceRegistry:
			if err := s.startRegistry(ctx); err != nil {
				return nil, fmt.Errorf("failed to start registry: %w", err)
			}
			if !s.registryReused {
				if err := s.UploadImages(); err != nil {
					return nil, fmt.Errorf("failed to upload images: %w", err)
				}
			} else {
				logrus.Info("Skipping image bundle upload (registry container was reused)")
			}
		case ServiceGitServer:
			if err := s.startGitServer(ctx); err != nil {
				return nil, fmt.Errorf("failed to start git server: %w", err)
			}
		case ServicePrometheus:
			if err := s.startPrometheus(ctx); err != nil {
				return nil, fmt.Errorf("failed to start prometheus: %w", err)
			}
		case ServiceTracing:
			if err := s.startJaeger(ctx); err != nil {
				return nil, fmt.Errorf("failed to start jaeger: %w", err)
			}
		default:
			return nil, fmt.Errorf("unknown service: %q", svc)
		}
	}
	return s, nil
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

// serviceContainerNames maps each Service to its podman container name.
var serviceContainerNames = map[Service]string{
	ServiceRegistry:   registryContainerName,
	ServiceGitServer:  gitServerContainerName,
	ServicePrometheus: prometheusContainerName,
	ServiceTracing:    jaegerContainerName,
}

// StopServices force-removes the containers for the requested satellite services.
func StopServices(services []Service) error {
	for _, svc := range services {
		name, ok := serviceContainerNames[svc]
		if !ok {
			return fmt.Errorf("unknown service: %q", svc)
		}
		logrus.Infof("Stopping satellite container %s", name)
		if err := podmanRemove(name); err != nil {
			logrus.Warnf("Could not remove %s: %v", name, err)
		}
	}
	return nil
}

func podmanRemove(containerName string) error {
	cmd := exec.Command("podman", "rm", "-f", "-v", containerName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
