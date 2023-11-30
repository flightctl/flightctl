package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/agent"
	"github.com/flightctl/flightctl/internal/agent/controller"
	"k8s.io/klog/v2"
)

func main() {

	serverUrl := flag.String("server", "", "device server URL")
	dataDir := flag.String("data-dir", "/var/lib/flightctl", "device agent data directory")

	fetchSpecInterval := flag.Duration("fetch-spec-interval", agent.DefaultFetchSpecInterval, "Duration between two reads of the remote device spec")
	statusUpdateInterval := flag.Duration("status-update-interval", agent.DefaultStatusUpdateInterval, "Duration between two status updates")
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

	agentInstance := agent.NewDeviceAgent(*serverUrl, *serverUrl, *dataDir).
		AddController(controller.NewSystemInfoController()).
		AddController(controller.NewContainerController()).
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
