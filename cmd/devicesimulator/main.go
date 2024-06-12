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
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
)

const (
	appName = "flightctl"
)

func defaultConfigFilePath() string {
	return filepath.Join(util.MustString(os.UserHomeDir), "."+appName, "agent.yaml")
}

func defaultDataDir() string {
	return filepath.Join(util.MustString(os.UserHomeDir), "."+appName, "data")
}

func main() {
	log := flightlog.InitLogs()
	configFile := flag.String("config", defaultConfigFilePath(), "path of the agent configuration template")
	dataDir := flag.String("data-dir", defaultDataDir(), "directory for storing simulator data")
	numDevices := flag.Int("count", 1, "number of devices to simulate")
	metricsAddr := flag.String("metrics", "localhost:9093", "address for the metrics endpoint")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		fmt.Println("This program starts a devicesimulator with the specified configuration. Below are the available flags:")
		flag.PrintDefaults()
	}
	flag.Parse()

	agentConfigTemplate := agent.NewDefault()
	agentConfigTemplate.ConfigDir = filepath.Dir(*configFile)
	if err := agentConfigTemplate.ParseConfigFile(*configFile); err != nil {
		log.Fatalf("Error parsing config: %v", err)
	}
	if err := agentConfigTemplate.Validate(); err != nil {
		log.Fatalf("Error validating config: %v", err)
	}

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
		certDir := filepath.Join(agentConfigTemplate.ConfigDir, "certs")
		agentDir := filepath.Join(*dataDir, agentName)
		for _, filename := range []string{"ca.crt", "client-enrollment.crt", "client-enrollment.key"} {
			if err := copyFile(filepath.Join(certDir, filename), filepath.Join(agentDir, filename)); err != nil {
				log.Fatalf("copying %s: %v", filename, err)
			}
		}

		cfg := agent.NewDefault()
		cfg.ConfigDir = agentConfigTemplate.ConfigDir
		cfg.DataDir = agentDir
		cfg.EnrollmentService = agentConfigTemplate.EnrollmentService
		cfg.ManagementService = agent.ManagementService{
			Config: client.Config{
				Service: agentConfigTemplate.ManagementService.Config.Service,
				AuthInfo: client.AuthInfo{
					ClientCertificate:     filepath.Join(agentDir, agent.GeneratedCertFile),
					ClientCertificateData: []byte{},
					ClientKey:             filepath.Join(agentDir, agent.KeyFile),
					ClientKeyData:         []byte{},
				},
			},
		}
		cfg.SpecFetchInterval = agentConfigTemplate.SpecFetchInterval
		cfg.StatusUpdateInterval = agentConfigTemplate.StatusUpdateInterval
		cfg.TPMPath = ""
		cfg.LogPrefix = agentName
		cfg.SetEnrollmentMetricsCallback(rpcMetricsCallback)
		if err := cfg.Complete(); err != nil {
			log.Fatalf("agent config %d: %v", i, err)
		}
		if err := cfg.Validate(); err != nil {
			log.Fatalf("agent config %d: %v", i, err)
		}

		log := flightlog.NewPrefixLogger(agentName)
		agents[i] = agent.New(log, cfg)
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
			time.Sleep(time.Duration(rand.Float64() * float64(agentConfigTemplate.StatusUpdateInterval))) //nolint:gosec

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
