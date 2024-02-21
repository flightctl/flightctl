package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/agent"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
)

func main() {
	log := log.InitLogs()
	dataDir := flag.String("data-dir", "/etc/flightctl", "device agent data directory")
	agentConfig, _ := agent.LoadOrGenerate(filepath.Join(*dataDir, "config.yaml"))

	managementServerEndpoint := flag.String("management-endpoint", agentConfig.ManagementServerEndpoint, "device server endpoint")
	enrollmentServerEndpoint := flag.String("enrollment-endpoint", agentConfig.EnrollmentServerEndpoint, "enrollment server endpoint")
	enrollmentUIEndpoint := flag.String("enrollment-ui-endpoint", agentConfig.EnrollmentUIEndpoint, "enrollment UI endpoint")
	tpmPath := flag.String("tpm", agentConfig.TPMPath, "Path to TPM device")
	fetchSpecInterval := flag.Duration("fetch-spec-interval", time.Duration(agentConfig.FetchSpecInterval), "Duration between two reads of the remote device spec")
	statusUpdateInterval := flag.Duration("status-update-interval", time.Duration(agentConfig.StatusUpdateInterval), "Duration between two status updates")
	flag.Parse()

	log.Infoln("starting flightctl device agent")
	defer log.Infoln("flightctl device agent stopped")

	log.Infoln("command line flags:")
	flag.CommandLine.VisitAll(func(flg *flag.Flag) {
		log.Infof("  %s=%s", flg.Name, flg.Value)
	})

	if len(*managementServerEndpoint) == 0 {
		log.Fatalf("flightctl server URL is required")
	}

	if *enrollmentServerEndpoint == "" {
		log.Warningf("flightctl enrollment endpoint is missing, using management endpoint")
		*enrollmentServerEndpoint = *managementServerEndpoint
	}

	cfg := agent.Config{
		ManagementServerEndpoint: *managementServerEndpoint,
		EnrollmentServerEndpoint: *enrollmentServerEndpoint,
		EnrollmentUIEndpoint:     *enrollmentUIEndpoint,
		CertDir:                  filepath.Join(*dataDir, "certs"),
		TPMPath:                  *tpmPath,
		FetchSpecInterval:        util.Duration(*fetchSpecInterval),
		StatusUpdateInterval:     util.Duration(*statusUpdateInterval),
	}

	agentInstance := agent.New(log, &cfg)

	ctx, cancel := context.WithCancel(context.Background())
	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigShutdown
		signal.Stop(sigShutdown)
		log.Printf("Shutdown signal received (%v).", sig)
		cancel()
	}()

	if err := agentInstance.Run(ctx); err != nil {
		log.Fatalf("running device agent: %v", err)
	}
}
