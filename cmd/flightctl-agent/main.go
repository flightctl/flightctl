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

	defaults := agent.NewDefault()
	dataDir := flag.String("data-dir", "/etc/flightctl", "device agent data directory")
	managementServerEndpoint := flag.String("management-endpoint", defaults.ManagementServerEndpoint, "device server endpoint")
	enrollmentServerEndpoint := flag.String("enrollment-endpoint", defaults.EnrollmentServerEndpoint, "enrollment server endpoint")
	enrollmentUIEndpoint := flag.String("enrollment-ui-endpoint", defaults.EnrollmentUIEndpoint, "enrollment UI endpoint")
	tpmPath := flag.String("tpm", defaults.TPMPath, "Path to TPM device")
	fetchSpecInterval := flag.Duration("fetch-spec-interval", time.Duration(defaults.FetchSpecInterval), "Duration between two reads of the remote device spec")
	statusUpdateInterval := flag.Duration("status-update-interval", time.Duration(defaults.StatusUpdateInterval), "Duration between two status updates")
	flag.Parse()

	agentConfig, err := agent.LoadOrGenerate(filepath.Join(*dataDir, "config.yaml"))
	if err != nil {
		log.Fatalf("loading or generating agent config: %v", err)
	}

	// Now that we have a config, we can override the command line flags with the default values from the config%
	*managementServerEndpoint = agentConfig.ManagementServerEndpoint
	*enrollmentServerEndpoint = agentConfig.EnrollmentServerEndpoint
	*enrollmentUIEndpoint = agentConfig.EnrollmentUIEndpoint
	*tpmPath = agentConfig.TPMPath
	*fetchSpecInterval = time.Duration(agentConfig.FetchSpecInterval)
	*statusUpdateInterval = time.Duration(agentConfig.StatusUpdateInterval)

	// and parse again to handle any config-file overrides
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
