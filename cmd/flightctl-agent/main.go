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
	"github.com/flightctl/flightctl/internal/agent/controller"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/log"
)

func main() {
	log := log.InitLogs()
	dataDir := flag.String("data-dir", "/etc/flightctl", "device agent data directory")

	agentConfig, _ := agent.LoadOrGenerate(filepath.Join(*dataDir, "config.yaml"))

	serverUrl := flag.String("server", agentConfig.Agent.Server, "device server URL")
	enrollmentUiUrl := flag.String("enrollment-ui", agentConfig.Agent.EnrollmentUi, "enrollment UI URL base")
	tpmPath := flag.String("tpm", agentConfig.Agent.TpmPath, "Path to TPM device")

	fetchSpecInterval := flag.Duration("fetch-spec-interval", time.Duration(agentConfig.Agent.FetchSpecInterval), "Duration between two reads of the remote device spec")
	statusUpdateInterval := flag.Duration("status-update-interval", time.Duration(agentConfig.Agent.StatusUpdateInterval), "Duration between two status updates")
	flag.Parse()

	log.Infoln("starting flightctl device agent")
	defer log.Infoln("flightctl device agent stopped")

	log.Infoln("command line flags:")
	flag.CommandLine.VisitAll(func(flg *flag.Flag) {
		log.Infof("  %s=%s", flg.Name, flg.Value)
	})

	if *serverUrl == "" {
		log.Fatalf("flightctl server URL is required")
	}

	log.Infoln("setting up TPM")
	var err error
	var tpmChannel *tpm.TPM
	if len(*tpmPath) > 0 {
		tpmChannel, err = tpm.OpenTPM(*tpmPath)
		if err != nil {
			log.Errorf("opening TPM channel: %v", err)
		}
	} else {
		tpmChannel, err = tpm.OpenTPMSimulator()
		if err != nil {
			log.Errorf("opening TPM simulator channel: %v", err)
		}
	}

	if *enrollmentUiUrl == "" {
		log.Warningf("flightctl enrollment UI URL is missing, using enrollment server URL")
		*enrollmentUiUrl = *serverUrl
	}

	agentInstance := agent.NewDeviceAgent(*serverUrl, *serverUrl, *enrollmentUiUrl, *dataDir, log).
		AddController(controller.NewSystemInfoController(tpmChannel)).
		AddController(controller.NewContainerController()).
		AddController(controller.NewSystemDController()).
		SetFetchSpecInterval(*fetchSpecInterval, 0).
		SetStatusUpdateInterval(*statusUpdateInterval, 0)

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
