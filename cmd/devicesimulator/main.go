package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/agent"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
)

func main() {
	log := log.InitLogs()
	managementEndpoint := flag.String("management-endpoint", "https://localhost:3333", "device server URL")
	metricsAddr := flag.String("metrics", "localhost:9093", "address for the metrics endpoint")
	certDir := flag.String("certs", config.CertificateDir(), "absolute path to the certificate dir")
	numDevices := flag.Int("count", 1, "number of devices to simulate")
	fetchSpecInterval := flag.Duration("fetch-spec-interval", agent.DefaultFetchSpecInterval, "Duration between two reads of the remote device spec")
	statusUpdateInterval := flag.Duration("status-update-interval", agent.DefaultStatusUpdateInterval, "Duration between two status updates")
	tpmPath := flag.String("tpm", "", "Path to TPM device")
	flag.Parse()

	log.Infoln("starting device simulator")
	defer log.Infoln("device simulator stopped")

	log.Infoln("command line flags:")
	flag.CommandLine.VisitAll(func(flg *flag.Flag) {
		log.Infof("  %s=%s", flg.Name, flg.Value)
	})

	log.Infoln("setting up metrics endpoint")
	setupMetricsEndpoint(*metricsAddr)

	log.Infoln("creating agents")
	agents := make([]*agent.Agent, *numDevices)
	for i := 0; i < *numDevices; i++ {
		agentName := fmt.Sprintf("device-%04d", i)
		agentDir := filepath.Join(*certDir, agentName)
		for _, filename := range []string{"ca.crt", "client-enrollment.crt", "client-enrollment.key"} {
			if err := copyFile(filepath.Join(*certDir, filename), filepath.Join(agentDir, filename)); err != nil {
				log.Fatalf("copying %s: %v", filename, err)
			}
		}
		enrollmentUiUrl := ""

		cfg := agent.Config{
			ManagementEndpoint:   *managementEndpoint,
			EnrollmentEndpoint:   enrollmentUiUrl,
			CertDir:              agentDir,
			TPMPath:              *tpmPath,
			FetchSpecInterval:    util.Duration(*fetchSpecInterval),
			StatusUpdateInterval: util.Duration(*statusUpdateInterval),
		}

		agents[i] = agent.New(&cfg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigShutdown
		signal.Stop(sigShutdown)
		log.Printf("Shutdown signal received (%v).", sig)
		cancel()
	}()

	log.Infoln("running agents")
	wg := new(sync.WaitGroup)
	wg.Add(*numDevices)
	for i := 0; i < *numDevices; i++ {
		go func(i int) {
			defer wg.Done()

			// stagger the start of each agent
			time.Sleep(time.Duration(rand.Float64() * float64(*statusUpdateInterval))) //nolint:gosec

			activeAgents.Inc()
			err := agents[i].Run(ctx)
			if err != nil {
				log.Errorf("%s: %v", agents[i].GetLogPrefix(), err)
			}
			activeAgents.Dec()
		}(i)
	}
	wg.Wait()
}

func copyFile(from, to string) error {
	if _, err := os.Stat(from); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	r, err := os.Open(from)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.Create(to)
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = io.Copy(w, r)
	return err
}
