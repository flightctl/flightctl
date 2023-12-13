package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/flightctl/flightctl/internal/agent"
	"github.com/flightctl/flightctl/internal/agent/controller"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/tpm"
	"k8s.io/klog/v2"
)

func main() {
	dataDir := flag.String("data-dir", "/etc/flightctl", "device agent data directory")

	agentConfig, _ := config.LoadOrGenerate(filepath.Join(*dataDir, "config.yaml"))

	serverUrl := flag.String("server", agentConfig.Agent.Server, "device server URL")
	enrollmentUiUrl := flag.String("enrollment-ui", agentConfig.Agent.EnrollmentUi, "enrollment UI URL base")
	tpmPath := flag.String("tpm", agentConfig.Agent.TpmPath, "Path to TPM device")

	fetchSpecInterval := flag.Duration("fetch-spec-interval", agentConfig.Agent.FetchSpecInterval, "Duration between two reads of the remote device spec")
	statusUpdateInterval := flag.Duration("status-update-interval", agentConfig.Agent.StatusUpdateInterval, "Duration between two status updates")
	flag.Parse()

	klog.Infoln("starting flightctl device agent")
	defer klog.Infoln("flightctl device agent stopped")

	klog.Infoln("command line flags:")
	flag.CommandLine.VisitAll(func(flg *flag.Flag) {
		klog.Infof("  %s=%s", flg.Name, flg.Value)
	})

	if *serverUrl == "" {
		klog.Fatalf("flightctl server URL is required")
	}

	klog.Infoln("setting up TPM")
	var err error
	var tpmChannel *tpm.TPM
	if len(*tpmPath) > 0 {
		tpmChannel, err = tpm.OpenTPM(*tpmPath)
		if err != nil {
			klog.Errorf("opening TPM channel: %v", err)
		}
	} else {
		tpmChannel, err = tpm.OpenTPMSimulator()
		if err != nil {
			klog.Errorf("opening TPM simulator channel: %v", err)
		}
	}

	if *enrollmentUiUrl == "" {
		klog.Warningf("flightctl enrollment UI URL is missing, using enrollment server URL")
		*enrollmentUiUrl = *serverUrl
	}

	agentInstance := agent.NewDeviceAgent(*serverUrl, *serverUrl, *enrollmentUiUrl, *dataDir).
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
