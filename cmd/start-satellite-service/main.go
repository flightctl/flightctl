package main

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/test/e2e/infra/satellite"
	"github.com/sirupsen/logrus"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: start-satellite-service <service>...\n")
		fmt.Fprintf(os.Stderr, "  services: all, registry, git-server, prometheus\n")
		os.Exit(1)
	}

	services, err := parseServices(os.Args[1:])
	if err != nil {
		logrus.Fatal(err)
	}

	svcs, err := satellite.StartServices(context.Background(), services)
	if err != nil {
		logrus.Fatalf("Failed to start services: %v", err)
	}

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
		default:
			return nil, fmt.Errorf("unknown service %q; valid values: all, registry, git-server, prometheus", arg)
		}
	}
	return services, nil
}
