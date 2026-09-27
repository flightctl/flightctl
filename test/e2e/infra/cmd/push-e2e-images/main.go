package main

import (
	"context"
	"os"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/sirupsen/logrus"
)

func main() {
	ctx := context.Background()
	if err := setup.EnsureDefaultProviders(nil); err != nil {
		logrus.Fatalf("push e2e images: providers: %v", err)
	}
	if _, err := auxiliary.StartServices(ctx, []auxiliary.Service{auxiliary.ServiceRegistry}); err != nil {
		logrus.Fatalf("push e2e images: %v", err)
	}
	os.Exit(0)
}
