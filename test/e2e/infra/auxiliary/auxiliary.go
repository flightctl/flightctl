// Package auxiliary provides shared testcontainer-based services for E2E tests.
// These run the same regardless of deployment (K8s or Quadlet): registry, git server, prometheus, jaeger.
// For deployment-specific infrastructure (where Flight Control runs), see the parent infra package
// and infra/k8s, infra/quadlet.
package auxiliary

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/sirupsen/logrus"
)

var (
	once sync.Once
	svcs *Services
)

// Services holds the E2E aux services (registry, git, prometheus, jaeger, keycloak).
// Same for all deployment types; created once and reused. Each service is nil until started.
// reuse is kept so Cleanup can no-op when reuse=true (containers stay running for the next run).
type Services struct {
	Registry   *Registry
	GitServer  *GitServer
	Prometheus *Prometheus
	Jaeger     *Jaeger
	Keycloak   *Keycloak

	reuse bool
}

// Service identifies an aux service that can be started individually.
type Service string

const (
	ServiceRegistry   Service = "registry"
	ServiceGitServer  Service = "git-server"
	ServicePrometheus Service = "prometheus"
	ServiceTracing    Service = "tracing"
	ServiceKeycloak   Service = "keycloak"
)

// AllServices is the default set of shared aux services (started by Get(ctx)).
// Does not include ServiceTracing; use infra.TracingProvider for opt-in tracing.
var AllServices = []Service{ServiceRegistry, ServiceGitServer, ServicePrometheus}

// Get returns the aux services, starting all of them if needed (singleton).
func Get(ctx context.Context) *Services {
	once.Do(func() {
		ConfigureDockerHost()
		var err error
		svcs, err = StartServices(ctx, AllServices)
		if err != nil {
			logrus.Fatalf("failed to start aux services: %v", err)
		}
	})
	return svcs
}

// StartServices starts only the requested aux services with reuse=true.
// For registry, image bundles are uploaded when the container is freshly created (not reused).
func StartServices(ctx context.Context, services []Service) (*Services, error) {
	network := GetDockerNetwork()
	reuse := true
	s := &Services{reuse: reuse}
	for _, svc := range services {
		switch svc {
		case ServiceRegistry:
			s.Registry = &Registry{}
			if err := s.Registry.Start(ctx, network, reuse); err != nil {
				return nil, fmt.Errorf("failed to start registry: %w", err)
			}
			if !s.Registry.Reused {
				if err := s.UploadImages(); err != nil {
					return nil, fmt.Errorf("failed to upload images: %w", err)
				}
				if err := s.UploadCharts(); err != nil {
					return nil, fmt.Errorf("failed to upload charts: %w", err)
				}
				if err := s.UploadQuadlets(); err != nil {
					return nil, fmt.Errorf("failed to upload quadlets: %w", err)
				}
			} else {
				logrus.Info("Skipping artifact upload (registry container was reused)")
			}
		case ServiceGitServer:
			s.GitServer = &GitServer{}
			if err := s.GitServer.Start(ctx, network, reuse); err != nil {
				return nil, fmt.Errorf("failed to start git server: %w", err)
			}
		case ServicePrometheus:
			s.Prometheus = &Prometheus{}
			if err := s.Prometheus.Start(ctx, network, reuse); err != nil {
				return nil, fmt.Errorf("failed to start prometheus: %w", err)
			}
		case ServiceTracing:
			s.Jaeger = &Jaeger{}
			if err := s.Jaeger.Start(ctx, network, reuse); err != nil {
				return nil, fmt.Errorf("failed to start jaeger: %w", err)
			}
		case ServiceKeycloak:
			s.Keycloak = &Keycloak{}
			if err := s.Keycloak.Start(ctx, network, reuse); err != nil {
				return nil, fmt.Errorf("failed to start keycloak: %w", err)
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
		logrus.Info("Aux reuse enabled: leaving containers running")
		return
	}
	logrus.Info("Terminating aux containers")
	ShutdownAll(ctx)
}

// serviceContainerNames maps each Service to its podman container name.
var serviceContainerNames = map[Service]string{
	ServiceRegistry:   registryContainerName,
	ServiceGitServer:  gitServerContainerName,
	ServicePrometheus: prometheusContainerName,
	ServiceTracing:    jaegerContainerName,
	ServiceKeycloak:   keycloakContainerName,
}

// StopServices force-removes the containers for the requested aux services.
func StopServices(services []Service) error {
	for _, svc := range services {
		name, ok := serviceContainerNames[svc]
		if !ok {
			return fmt.Errorf("unknown service: %q", svc)
		}
		logrus.Infof("Stopping aux container %s", name)
		if err := podmanRemove(name); err != nil {
			logrus.Warnf("Could not remove %s: %v", name, err)
		}
		if svc == ServiceRegistry {
			logrus.Infof("Stopping satellite container %s", privateRegistryContainerName)
			if err := podmanRemove(privateRegistryContainerName); err != nil {
				logrus.Warnf("Could not remove %s: %v", privateRegistryContainerName, err)
			}
		}
	}
	return nil
}

func podmanRemove(containerName string) error {
	//nolint:gosec // G204: containerName is only from serviceContainerNames or package constants (fixed e2e aux names).
	cmd := exec.Command(containerRuntimeCLIName(), "rm", "-f", "-v", containerName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
