package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/util/wait"
)

var (
	// post-incremented for each agent created
	agentCounter int
	// command line flags
	cfg *viper.Viper
)

const (
	// backoff for polling for enrollment approval
	approveAgentBackoff = 10 * time.Second
	// timeout for polling for enrollment approval
	approveAgentTimeout = 5 * time.Minute
)

func main() {
	// command line flags
	cfg = viper.New()
	cfg.SetEnvPrefix("FLIGHTCTL_SIMULATOR")
	cfg.AutomaticEnv()
	// paths are relative to the project root
	cfg.SetDefault("config", filepath.Join(util.GetHomeDir(), ".flightctl", "config.yaml"))
	cfg.SetDefault("data-dir", filepath.Join(util.GetHomeDir(), ".flightctl", "data"))
	cfg.SetDefault("agent-startup-jitter", -1*time.Second) // disabled by default
	cfg.SetDefault("log-level", "info")
	cfg.SetDefault("metrics", "localhost:9093")
	cfg.SetDefault("max-concurrency", 100)
	cfg.SetDefault("stop-after", 0*time.Second) // disabled by default

	rootCmd := &cobra.Command{
		Use:   "devicesimulator",
		Short: "device simulator",
		Run: func(cmd *cobra.Command, args []string) {
			log := log.InitLogs(cfg.GetString("log-level"))
			defer log.Infoln("device simulator stopped")

			log.Infoln("command line flags:")
			cmd.Flags().VisitAll(func(flg *pflag.Flag) {
				log.Infof("  %s=%s", flg.Name, flg.Value)
			})

			log.Infoln("starting device simulator")

			log.Infoln("setting up metrics endpoint")
			setupMetricsEndpoint(log, cfg.GetString("metrics"))

			flightctlClient, err := client.NewFromConfigFile(cfg.GetString("config"))
			if err != nil {
				log.Fatalf("failed creating flightctl client: %v", err)
			}
			log.Infoln("flightctl client created")
			initialLabels := make(map[string]string)
			for _, label := range cfg.GetStringSlice("label") {
				parts := strings.SplitN(label, "=", 2)
				if len(parts) != 2 {
					log.Fatalf("invalid label: %s", label)
				}
				initialLabels[parts[0]] = parts[1]
			}

			// if a fleet is defined on the command line, create it
			if err := createSimulatorFleet(context.Background(), flightctlClient, log); err != nil {
				log.Warnf("Failed to create simulator fleet: %v", err)
			}
			agents, err := createAgents(
				log,
				cfg.GetString("data-dir"),
				cfg.GetInt("count"),
				cfg.GetInt("initial-device-index"),
				cfg.GetDuration("agent-startup-jitter"),
				cfg.GetStringSlice("source-ips"),
				initialLabels,
			)
			if err != nil {
				log.Fatalf("failed creating agents: %v", err)
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

			if cfg.GetDuration("stop-after") > 0 {
				time.AfterFunc(cfg.GetDuration("stop-after"), func() {
					log.Infof("Stopping after %s.", cfg.GetDuration("stop-after"))
					cancel()
				})
			}

			log.Infoln("running agents")
			runAgents(ctx, log, agents)
		},
	}

	rootCmd.Flags().String("config", "", "flightctl client config file")
	rootCmd.Flags().String("data-dir", "", "data directory for agents")
	rootCmd.Flags().Int("count", 1, "number of devices to simulate")
	rootCmd.Flags().Int("initial-device-index", 0, "initial device index")
	rootCmd.Flags().Duration("agent-startup-jitter", -1, "if positive, the agents will start staggered over this duration")
	rootCmd.Flags().StringSlice("label", []string{}, "labels to add to the device")
	rootCmd.Flags().String("log-level", "", "log level (debug, info, warn, error)")
	rootCmd.Flags().String("metrics", "", "address for the metrics endpoint")
	rootCmd.Flags().String("output", "", "output file")
	rootCmd.Flags().Int("max-concurrency", 100, "maximum number of concurrent agent starts")
	rootCmd.Flags().StringSlice("source-ips", []string{}, "source IP addresses to use for the agents")
	rootCmd.Flags().Duration("stop-after", 0, "stop after this duration")
	if err := cfg.BindPFlags(rootCmd.Flags()); err != nil {
		fmt.Printf("failed binding pflags: %v", err)
		os.Exit(1)
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Printf("failed executing root command: %v", err)
		os.Exit(1)
	}
}

func createAgents(log logr.Logger, dataDir string, count int, initialDeviceIndex int, startupJitter time.Duration, sourceIPs []string, initialLabels map[string]string) ([]*agent.Agent, error) {
	log.Infoln("creating agents")
	if _, err := os.Stat(dataDir); err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(dataDir, 0755); err != nil {
				return nil, fmt.Errorf("creating data directory %s: %w", dataDir, err)
			}
		} else {
			return nil, fmt.Errorf("stating data directory %s: %w", dataDir, err)
		}
	}

	enrollmentServer, managementServer, err := getServersFromClient(cfg.GetString("config"))
	if err != nil {
		return nil, fmt.Errorf("getting servers from client config: %w", err)
	}

	agents := make([]*agent.Agent, count)
	for i := 0; i < count; i++ {
		agentName := fmt.Sprintf("device-%05d", i+initialDeviceIndex)
		log := log.WithName(agentName)
		agentDir := filepath.Join(dataDir, agentName)
		if err := os.MkdirAll(agentDir, 0755); err != nil {
			return nil, fmt.Errorf("creating agent directory %s: %w", agentDir, err)
		}

		var sourceIP string
		if len(sourceIPs) > 0 {
			sourceIP = sourceIPs[i%len(sourceIPs)]
		}

		agentConfig := &agent.Config{
			ManagementServerEndpoint: managementServer,
			EnrollmentServerEndpoint: enrollmentServer,
			DataDir:                  agentDir,
			CertDir:                  agentDir,
			Insecure:                 true,
			TestRootDir:              agentDir,
			Labels:                   initialLabels,
			SourceIPAddress:          sourceIP,
		}
		if startupJitter > 0 {
			agentConfig.StartupJitter = startupJitter
		}

		cfg, err := agentConfig.Validate()
		if err != nil {
			return nil, fmt.Errorf("agent config %d: %w", i, err)
		}

		agent, err := agent.New(log, cfg)
		if err != nil {
			return nil, fmt.Errorf("creating agent %d: %w", i, err)
		}
		agents[i] = agent
	}
	return agents, nil
}

func getServersFromClient(configPath string) (string, string, error) {
	clientConfig, err := client.LoadConfig(configPath)
	if err != nil {
		return "", "", fmt.Errorf("loading client config: %w", err)
	}
	if clientConfig.CurrentContext == "" && len(clientConfig.Contexts) > 0 {
		for k := range clientConfig.Contexts {
			clientConfig.CurrentContext = k
			break
		}
	}
	context := clientConfig.Contexts[clientConfig.CurrentContext]
	return context.Service.Server, context.Service.Server, nil
}

func runAgents(ctx context.Context, log logr.Logger, agents []*agent.Agent) {
	wg := new(sync.WaitGroup)
	wg.Add(len(agents))

	concurrency := make(chan struct{}, cfg.GetInt("max-concurrency"))

	for _, a := range agents {
		concurrency <- struct{}{}
		go func(a *agent.Agent) {
			defer func() {
				wg.Done()
				<-concurrency
			}()
			log := log.WithName(a.GetLogPrefix())

			err := a.Run(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Error(err, "agent run failed")
			}
		}(a)
		go approveAgent(ctx, log, a)
	}
	wg.Wait()
}

func approveAgent(ctx context.Context, log logr.Logger, agent *agent.Agent) {
	log = log.WithName(agent.GetLogPrefix())

	// wait for the agent to create the enrollment request
	err := wait.PollImmediateInfiniteWithContext(ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		return agent.GetEnrollmentRequest() != nil, nil
	})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Errorf("error waiting for enrollment request: %v", err)
		}
		return
	}

	cfgFile := cfg.GetString("config")
	client, err := client.NewFromConfigFile(cfgFile)
	if err != nil {
		log.Errorf("creating management client: %v", err)
		return
	}

	// approve the agent
	err = wait.PollUntilContextTimeout(ctx, approveAgentBackoff, approveAgentTimeout, true, func(ctx context.Context) (bool, error) {
		enrollmentRequest, err := client.GetEnrollmentRequest(ctx, agent.GetEnrollmentRequest().Name)
		if err != nil {
			log.Errorf("error getting enrollment request: %v", err)
			return false, nil
		}
		if enrollmentRequest.Status.Approval == nil {
			log.Info("Approving enrollment request")
			approval := v1alpha1.EnrollmentRequestApproval{
				Approved: true,
				Labels:   &agent.GetConfig().Labels,
			}
			err = client.ApproveEnrollmentRequest(ctx, agent.GetEnrollmentRequest().Name, approval)
			if err != nil {
				log.Errorf("error approving enrollment: %v", err)
				return false, nil
			}
			return true, nil
		}
		return true, nil
	})
	if err != nil {
		log.Errorf("Error approving device %s enrollment: %v", agent.GetEnrollmentRequest().Name, err)
	}
}

// create a fleet for the simulator if it does not exist
func createSimulatorFleet(ctx context.Context, client *client.Client, log logr.Logger) error {
	fleetName := "simulator-disk-monitoring"
	log.Info("Creating fleet configuration: " + fleetName)

	fleetYAML, err := os.ReadFile("examples/fleet-disk-simulator.yaml")
	if err != nil {
		return fmt.Errorf("reading fleet YAML file examples/fleet-disk-simulator.yaml: %w", err)
	}

	fleet, err := client.CreateFleetFromReader(ctx, strings.NewReader(string(fleetYAML)))
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			log.Info("Fleet already exists")
			return nil
		}
		return fmt.Errorf("creating fleet: %w", err)
	}
	log.Infof("Fleet %s created", fleet.Metadata.Name)
	return nil
}

func setupMetricsEndpoint(log logr.Logger, metricsAddr string) {
	go func() {
		router := chi.NewRouter()
		router.Use(middleware.RequestID)
		router.Use(middleware.RealIP)
		router.Use(middleware.Logger)
		router.Use(middleware.Recoverer)
		router.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(metricsAddr, router); err != nil {
			log.Errorf("starting metrics endpoint: %v", err)
		}
	}()
}