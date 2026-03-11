package infra

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

var traceableServiceNames = []ServiceName{
	ServiceAPI,
	ServiceWorker,
	ServicePeriodic,
	ServiceTelemetryGateway,
	ServiceAlertmanagerProxy,
	ServiceAlertExporter,
	ServiceImageBuilderAPI,
}

const serviceRestartTimeout = 2 * time.Minute

// TracingProvider starts/stops the tracing aux container and configures flightctl services accordingly.
type TracingProvider struct {
	infra     InfraProvider
	lifecycle ServiceLifecycleProvider
}

// NewTracingProvider returns a TracingProvider that uses the given infra and lifecycle.
func NewTracingProvider(infra InfraProvider, lifecycle ServiceLifecycleProvider) *TracingProvider {
	return &TracingProvider{infra: infra, lifecycle: lifecycle}
}

// StartTracing starts the tracing aux container and configures flightctl services to report to it.
func (p *TracingProvider) StartTracing(ctx context.Context) (*auxiliary.Services, error) {
	svcs, err := auxiliary.StartServices(ctx, []auxiliary.Service{auxiliary.ServiceTracing})
	if err != nil {
		return nil, fmt.Errorf("start tracing aux: %w", err)
	}
	if err := p.enableFlightctlTracing(svcs.JaegerOTLPEndpoint); err != nil {
		return nil, fmt.Errorf("enable flightctl tracing: %w", err)
	}
	logrus.Info("Configured flightctl services to report traces to ", svcs.JaegerOTLPEndpoint)
	return svcs, nil
}

// StopTracing reconfigures flightctl services to disable tracing, then stops the tracing aux container.
func (p *TracingProvider) StopTracing() error {
	if err := p.disableFlightctlTracing(); err != nil {
		return fmt.Errorf("disable flightctl tracing: %w", err)
	}
	logrus.Info("Disabled tracing on flightctl services")
	if err := auxiliary.StopServices([]auxiliary.Service{auxiliary.ServiceTracing}); err != nil {
		return fmt.Errorf("stop tracing aux: %w", err)
	}
	return nil
}

func (p *TracingProvider) enableFlightctlTracing(otlpEndpoint string) error {
	for _, name := range traceableServiceNames {
		content, err := p.infra.GetServiceConfig(name)
		if err != nil {
			logrus.Warnf("Skipping tracing config for %s: %v", name, err)
			continue
		}
		var cfg config.Config
		if err := yaml.Unmarshal([]byte(content), &cfg); err != nil {
			return fmt.Errorf("parse config for %s: %w", name, err)
		}
		cfg.Tracing = &config.TracingConfig{
			Enabled:  true,
			Endpoint: otlpEndpoint,
			Insecure: true,
		}
		if err := p.writeConfigAndRestart(name, &cfg); err != nil {
			return err
		}
		logrus.Infof("Configured tracing for %s → %s", name, otlpEndpoint)
	}
	return nil
}

func (p *TracingProvider) disableFlightctlTracing() error {
	for _, name := range traceableServiceNames {
		content, err := p.infra.GetServiceConfig(name)
		if err != nil {
			logrus.Warnf("Skipping tracing disable for %s: %v", name, err)
			continue
		}
		var cfg config.Config
		if err := yaml.Unmarshal([]byte(content), &cfg); err != nil {
			return fmt.Errorf("parse config for %s: %w", name, err)
		}
		cfg.Tracing = nil
		if err := p.writeConfigAndRestart(name, &cfg); err != nil {
			return err
		}
		logrus.Infof("Disabled tracing for %s", name)
	}
	return nil
}

func (p *TracingProvider) writeConfigAndRestart(name ServiceName, cfg *config.Config) error {
	updated, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config for %s: %w", name, err)
	}
	if err := p.infra.SetServiceConfig(name, "config.yaml", string(updated)); err != nil {
		return fmt.Errorf("set config for %s: %w", name, err)
	}
	if err := p.lifecycle.Restart(name); err != nil {
		return fmt.Errorf("restart %s: %w", name, err)
	}
	if err := p.lifecycle.WaitForReady(name, serviceRestartTimeout); err != nil {
		return fmt.Errorf("%s not ready after restart: %w", name, err)
	}
	return nil
}
