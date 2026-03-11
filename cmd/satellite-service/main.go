package main

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/satellite"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/sirupsen/logrus"
)

const usage = `Usage: satellite-service <command> <service>...
  commands: start, stop
  services: all, registry, git-server, prometheus, tracing`

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}

	command := os.Args[1]
	services, err := parseServices(os.Args[2:])
	if err != nil {
		logrus.Fatal(err)
	}

	switch command {
	case "start":
		runStart(services)
	case "stop":
		runStop(services)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n%s\n", command, usage)
		os.Exit(1)
	}
}

func runStart(services []satellite.Service) {
	ctx := context.Background()

	// Tracing uses TracingProvider (starts satellite + configures flightctl).
	if onlyTracing(services) {
		runStartTracing(ctx)
		return
	}

	// If "all", start non-tracing satellites then tracing via provider.
	if containsService(services, satellite.ServiceTracing) {
		others := withoutService(services, satellite.ServiceTracing)
		if len(others) > 0 {
			svcs, err := satellite.StartServices(ctx, others)
			if err != nil {
				logrus.Fatalf("Failed to start services: %v", err)
			}
			printServiceURLs(svcs)
		}
		runStartTracing(ctx)
		return
	}

	svcs, err := satellite.StartServices(ctx, services)
	if err != nil {
		logrus.Fatalf("Failed to start services: %v", err)
	}
	printServiceURLs(svcs)
}

func runStartTracing(ctx context.Context) {
	providers, err := setup.NewProvidersForEnvironment(nil)
	if err != nil {
		logrus.Fatalf("Could not create providers: %v", err)
	}
	provider := infra.NewTracingProvider(providers.Infra, providers.Lifecycle)
	svcs, err := provider.StartTracing(ctx)
	if err != nil {
		logrus.Fatalf("Failed to start tracing: %v", err)
	}
	if svcs.JaegerURL != "" {
		fmt.Printf("jaeger: %s\n", svcs.JaegerURL)
		fmt.Printf("jaeger-otlp: %s\n", svcs.JaegerOTLPEndpoint)
	}
}

func runStop(services []satellite.Service) {
	// Tracing uses TracingProvider (reconfigures flightctl then stops satellite).
	if onlyTracing(services) {
		runStopTracing()
		return
	}

	if containsService(services, satellite.ServiceTracing) {
		runStopTracing()
		services = withoutService(services, satellite.ServiceTracing)
		if len(services) == 0 {
			fmt.Println("Stopped satellite services")
			return
		}
	}

	if err := satellite.StopServices(services); err != nil {
		logrus.Fatalf("Failed to stop services: %v", err)
	}
	fmt.Println("Stopped satellite services")
}

func runStopTracing() {
	providers, err := setup.NewProvidersForEnvironment(nil)
	if err != nil {
		logrus.Fatalf("Could not create providers: %v", err)
	}
	provider := infra.NewTracingProvider(providers.Infra, providers.Lifecycle)
	if err := provider.StopTracing(); err != nil {
		logrus.Fatalf("Failed to stop tracing: %v", err)
	}
}

func printServiceURLs(svcs *satellite.Services) {
	if svcs.RegistryURL != "" {
		fmt.Printf("registry: %s\n", svcs.RegistryURL)
	}
	if svcs.GitServerURL != "" {
		fmt.Printf("git-server: %s\n", svcs.GitServerURL)
	}
	if svcs.PrometheusURL != "" {
		fmt.Printf("prometheus: %s\n", svcs.PrometheusURL)
	}
}

func onlyTracing(services []satellite.Service) bool {
	return len(services) == 1 && services[0] == satellite.ServiceTracing
}

func containsService(services []satellite.Service, name satellite.Service) bool {
	for _, s := range services {
		if s == name {
			return true
		}
	}
	return false
}

func withoutService(services []satellite.Service, name satellite.Service) []satellite.Service {
	out := make([]satellite.Service, 0, len(services))
	for _, s := range services {
		if s != name {
			out = append(out, s)
		}
	}
	return out
}

func parseServices(args []string) ([]satellite.Service, error) {
	var services []satellite.Service
	for _, arg := range args {
		switch arg {
		case "all":
			return satellite.AllServices, nil
		case "registry":
			services = append(services, satellite.ServiceRegistry)
		case "git-server":
			services = append(services, satellite.ServiceGitServer)
		case "prometheus":
			services = append(services, satellite.ServicePrometheus)
		case "tracing":
			services = append(services, satellite.ServiceTracing)
		default:
			return nil, fmt.Errorf("unknown service %q; valid values: all, registry, git-server, prometheus, tracing", arg)
		}
	}
	return services, nil
}
