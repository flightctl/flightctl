// Command preflight starts or stops testcontainers used by integration tests (Postgres, Redis, Alertmanager).
//
// Usage:
//
//	go run ./test/integration/preflight start
//	go run ./test/integration/preflight stop
//	go run ./test/integration/preflight migrate
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/test/integration/integrationstack"
	"github.com/sirupsen/logrus"
)

func main() {
	logrus.SetLevel(logrus.InfoLevel)
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: preflight start|stop|migrate")
		os.Exit(2)
	}
	ctx := context.Background()
	switch os.Args[1] {
	case "start":
		// Start containers only; migrations are run separately via 'migrate' command
		if err := integrationstack.EnsureContainersOnly(ctx); err != nil {
			logrus.Fatalf("preflight start: %v", err)
		}
	case "stop":
		if err := integrationstack.Stop(ctx); err != nil {
			logrus.Fatalf("preflight stop: %v", err)
		}
	case "migrate":
		// Run migrations using flightctl-db-migrate binary (same as production)
		if err := integrationstack.RunMigrations(ctx); err != nil {
			logrus.Fatalf("preflight migrate: %v", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}
