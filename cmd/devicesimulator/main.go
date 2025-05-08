package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/lifecycle"
	apiClient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"
)

const (
	appName = "flightctl"

	jsonFormat      = "json"
	yamlFormat      = "yaml"
	cliVersionTitle = "flightctl simulator version"
)

var (
	outputTypes = []string{jsonFormat, yamlFormat}
)

type simulatorConfig struct {
	configFile         string
	dataDir            string
	labels             []string
	numDevices         int
	initialDeviceIndex int
	metricsAddr        string
	stopAfter          time.Duration
	versionFormat      string
	logLevel           string
	showVersion        bool
	showHelp           bool
}

func defaultConfigFilePath() string {
	return filepath.Join(util.MustString(os.UserHomeDir), "."+appName, "agent.yaml")
}

func defaultDataDir() string {
	return filepath.Join(util.MustString(os.UserHomeDir), "."+appName, "data")
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", os.Args[0])
	fmt.Fprintln(os.Stderr, "Options:")
	fmt.Fprintln(os.Stderr, "  config=PATH            Path of the agent configuration template (default: ~/.flightctl/agent.yaml)")
	fmt.Fprintln(os.Stderr, "  data-dir=PATH          Directory for storing simulator data (default: ~/.flightctl/data)")
	fmt.Fprintln(os.Stderr, "  label=KEY=VALUE        Label applied to simulated devices (can be specified multiple times)")
	fmt.Fprintln(os.Stderr, "  count=NUM              Number of devices to simulate (default: 1)")
	fmt.Fprintln(os.Stderr, "  initial-device-index=NUM  Starting index for device name suffix (default: 0)")
	fmt.Fprintln(os.Stderr, "  metrics=ADDR           Address for the metrics endpoint (default: localhost:9093)")
	fmt.Fprintln(os.Stderr, "  stop-after=DURATION    Stop the simulator after the specified duration (e.g., 30s, 5m)")
	fmt.Fprintln(os.Stderr, "  log-level=LEVEL        Logger verbosity level (fatal, error, warn, warning, info, debug) (default: debug)")
	fmt.Fprintln(os.Stderr, "  version[=FORMAT]       Print version information and exit. FORMAT can be json or yaml (optional)")
	fmt.Fprintln(os.Stderr, "  help                   Display this help message and exit")
	fmt.Fprintln(os.Stderr, "\nExamples:")
	fmt.Fprintf(os.Stderr, "  %s count=3 label=region=us-east\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s version\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s version=json\n", os.Args[0])
}

func parseArguments() (*simulatorConfig, error) {
	// Create default config
	config := &simulatorConfig{
		configFile:         defaultConfigFilePath(),
		dataDir:            defaultDataDir(),
		labels:             []string{},
		numDevices:         1,
		initialDeviceIndex: 0,
		metricsAddr:        "localhost:9093",
		stopAfter:          0,
		logLevel:           "debug",
		versionFormat:      "",
		showVersion:        false,
		showHelp:           false,
	}

	if len(os.Args) < 2 {
		return config, nil
	}

	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		
		if arg == "version" {
			config.showVersion = true
			continue
		}
		
		if arg == "help" {
			config.showHelp = true
			continue
		}
		
		parts := strings.SplitN(arg, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid argument format: %s (expected key=value)", arg)
		}
		
		key := parts[0]
		value := parts[1]
		
		switch key {
		case "config":
			config.configFile = value
		case "data-dir":
			config.dataDir = value
		case "label":
			config.labels = append(config.labels, value)
		case "count":
			num, err := strconv.Atoi(value)
			if err != nil || num < 1 {
				return nil, fmt.Errorf("count must be a positive number")
			}
			config.numDevices = num
		case "initial-device-index":
			num, err := strconv.Atoi(value)
			if err != nil || num < 0 {
				return nil, fmt.Errorf("initial-device-index must be a non-negative number")
			}
			config.initialDeviceIndex = num
		case "metrics":
			config.metricsAddr = value
		case "stop-after":
			duration, err := time.ParseDuration(value)
			if err != nil {
				return nil, fmt.Errorf("invalid duration for stop-after: %v", err)
			}
			config.stopAfter = duration
		case "log-level":
			config.logLevel = value
		case "version":
			config.showVersion = true
			if value != "" && value != "true" {
				config.versionFormat = value
				if config.versionFormat != jsonFormat && config.versionFormat != yamlFormat {
					return nil, fmt.Errorf("invalid version format: %s (expected json, yaml, or empty)", config.versionFormat)
				}
			}
		case "help":
			config.showHelp = true
		default:
			return nil, fmt.Errorf("unknown argument: %s", key)
		}
	}
	
	return config, nil
}

func main() {
	log := flightlog.InitLogs()
	
	config, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n\n", err)
		printUsage()
		os.Exit(1)
	}
	
	if config.showHelp {
		printUsage()
		os.Exit(0)
	}
	
	if config.showVersion {
		if err := reportVersion(config.versionFormat); err != nil {
			fmt.Println(err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}
	
	logLvl, err := logrus.ParseLevel(config.logLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid log level: %s\n\n", config.logLevel)
		printUsage()
		os.Exit(1)
	}
	log.SetLevel(logLvl)

	log.Infoln("command line arguments:")
	log.Infof("  config=%s", config.configFile)
	log.Infof("  data-dir=%s", config.dataDir)
	for _, label := range config.labels {
		log.Infof("  label=%s", label)
	}
	log.Infof("  count=%d", config.numDevices)
	log.Infof("  initial-device-index=%d", config.initialDeviceIndex)
	log.Infof("  metrics=%s", config.metricsAddr)
	log.Infof("  stop-after=%s", config.stopAfter)
	log.Infof("  log-level=%s", config.logLevel)

	formattedLabels := formatLabels(&config.labels)

	agentConfigTemplate := createAgentConfigTemplate(config.dataDir, config.configFile)

	log.Infoln("starting device simulator")
	defer log.Infoln("device simulator stopped")

	log.Infoln("setting up metrics endpoint")
	setupMetricsEndpoint(config.metricsAddr)

	serviceClient, err := client.NewFromConfigFile(client.DefaultFlightctlClientConfigPath())
	if err != nil {
		log.Fatalf("Error creating service client: %v", err)
	}

	log.Infoln("creating agents")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agents, agentsFolders := createAgents(log, config.numDevices, config.initialDeviceIndex, agentConfigTemplate)

	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigShutdown
		signal.Stop(sigShutdown)
		log.Printf("Shutdown signal received (%v).", sig)
		cancel()
	}()

	log.Infoln("running agents")
	for i := 0; i < config.numDevices; i++ {
		// stagger the start of each agent
		time.Sleep(time.Duration(rand.Float64() * float64(agentConfigTemplate.StatusUpdateInterval))) //nolint:gosec
		agent := agents[i]
		go startAgent(ctx, agent, log, i)
		go approveAgent(ctx, log, serviceClient, agentsFolders[i], formattedLabels)
	}
	if config.stopAfter > 0 {
		time.AfterFunc(config.stopAfter, func() {
			log.Infoln("stopping simulator after duration")
			cancel()
		})
	}

	<-ctx.Done()
	log.Infoln("Simulator stopped.")
}

func reportVersion(versionFormat string) error {
	cliVersion := version.Get()
	switch versionFormat {
	case "":
		fmt.Printf("%s: %s\n", cliVersionTitle, cliVersion.String())
	case "yaml":
		marshalled, err := yaml.Marshal(&cliVersion)
		if err != nil {
			return fmt.Errorf("yaml marshalling error: %w", err)
		}
		fmt.Println(string(marshalled))
	case "json":
		marshalled, err := json.MarshalIndent(&cliVersion, "", "  ")
		if err != nil {
			return fmt.Errorf("json marshalling error: %w", err)
		}
		fmt.Println(string(marshalled))
	default:
		// There is a bug in the program if we hit this case.
		// However, we follow a policy of never panicking.
		return fmt.Errorf("VersionOptions were not validated: output=%q should have been rejected\n", versionFormat)
	}
	return nil
}

func startAgent(ctx context.Context, agent *agent.Agent, log *logrus.Logger, agentInstance int) {
	activeAgents.Inc()
	prefix := agent.GetLogPrefix()
	err := agent.Run(ctx)
	if err != nil {
		// agent timeout waiting for enrollment approval
		if wait.Interrupted(err) {
			log.Errorf("%s: agent timed out: %v", prefix, err)
		} else if ctx.Err() != nil {
			// normal teardown
			log.Infof("%s: agent stopped due to context cancellation.", prefix)
		} else {
			log.Fatalf("%s: %v", prefix, err)
		}
	}
	activeAgents.Dec()
}

func createAgentConfigTemplate(dataDir string, configFile string) *agent_config.Config {
	agentConfigTemplate := agent_config.NewDefault()
	agentConfigTemplate.ConfigDir = filepath.Dir(configFile)
	if err := agentConfigTemplate.ParseConfigFile(configFile); err != nil {
		log.Fatalf("Error parsing config: %v", err)
	}
	//create data directory if not exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("Error creating data directory: %v", err)
	}

	agentConfigTemplate.DataDir = dataDir
	if err := agentConfigTemplate.Complete(); err != nil {
		log.Fatalf("Error completing config: %v", err)
	}
	if err := agentConfigTemplate.Validate(); err != nil {
		log.Fatalf("Error validating config: %v", err)
	}

	return agentConfigTemplate
}

func createAgents(log *logrus.Logger, numDevices int, initialDeviceIndex int, agentConfigTemplate *agent_config.Config) ([]*agent.Agent, []string) {
	log.Infoln("creating agents")
	agents := make([]*agent.Agent, numDevices)
	agentsFolders := make([]string, numDevices)
	for i := 0; i < numDevices; i++ {
		agentName := fmt.Sprintf("device-%05d", initialDeviceIndex+i)
		certDir := filepath.Join(agentConfigTemplate.ConfigDir, "certs")
		agentDir := filepath.Join(agentConfigTemplate.DataDir, agentName)
		// Cleanup if exists and initialize the agent's expected
		os.RemoveAll(agentDir)
		if err := os.MkdirAll(filepath.Join(agentDir, agent_config.DefaultConfigDir), 0700); err != nil {
			log.Fatalf("Error creating directory: %v", err)
		}

		err := os.Setenv(client.TestRootDirEnvKey, agentDir)
		if err != nil {
			log.Fatalf("Error setting environment variable: %v", err)
		}
		for _, filename := range []string{"ca.crt", "client-enrollment.crt", "client-enrollment.key"} {
			if err := copyFile(filepath.Join(certDir, filename), filepath.Join(agentDir, agent_config.DefaultConfigDir, filename)); err != nil {
				log.Fatalf("copying %s: %v", filename, err)
			}
		}

		cfg := agent_config.NewDefault()
		cfg.DefaultLabels["alias"] = agentName
		cfg.ConfigDir = agent_config.DefaultConfigDir
		cfg.DataDir = agent_config.DefaultConfigDir
		cfg.EnrollmentService = config.EnrollmentService{}
		cfg.EnrollmentService.Config = *client.NewDefault()
		cfg.EnrollmentService.Config.Service = client.Service{
			Server:               agentConfigTemplate.EnrollmentService.Config.Service.Server,
			CertificateAuthority: filepath.Join(cfg.ConfigDir, agent_config.CacertFile),
		}
		cfg.EnrollmentService.Config.AuthInfo = client.AuthInfo{
			ClientCertificate: filepath.Join(cfg.ConfigDir, agent_config.EnrollmentCertFile),
			ClientKey:         filepath.Join(cfg.ConfigDir, agent_config.EnrollmentKeyFile),
		}
		cfg.SpecFetchInterval = agentConfigTemplate.SpecFetchInterval
		cfg.StatusUpdateInterval = agentConfigTemplate.StatusUpdateInterval
		cfg.TPMPath = ""
		cfg.LogPrefix = agentName

		// create managementService config
		cfg.ManagementService = config.ManagementService{}
		cfg.ManagementService.Config = *client.NewDefault()
		cfg.ManagementService.Service = client.Service{
			Server:               agentConfigTemplate.ManagementService.Config.Service.Server,
			CertificateAuthority: filepath.Join(cfg.ConfigDir, agent_config.CacertFile),
		}

		cfg.SetEnrollmentMetricsCallback(rpcMetricsCallback)
		if err := cfg.Complete(); err != nil {
			log.Fatalf("agent config %d: %v", i, err)
		}
		if err := cfg.Validate(); err != nil {
			log.Fatalf("agent config %d: %v", i, err)
		}

		logWithPrefix := flightlog.NewPrefixLogger(agentName)
		agents[i] = agent.New(logWithPrefix, cfg, "")
		agentsFolders[i] = agentDir
	}
	return agents, agentsFolders
}

func approveAgent(ctx context.Context, log *logrus.Logger, serviceClient *apiClient.ClientWithResponses, agentDir string, labels *map[string]string) {
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		// timeout after 30s and retry
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		log.Infof("Approving device enrollment if exists for agent %s", filepath.Base(agentDir))
		bannerFileData, err := readBannerFile(agentDir)
		if err != nil {
			log.Warnf("Error reading banner file: %v", err)
			return false, nil
		}
		enrollmentId := testutil.GetEnrollmentIdFromText(bannerFileData)
		if enrollmentId == "" {
			log.Warnf("No enrollment id found in banner file %s", bannerFileData)
			return false, nil
		}
		resp, err := serviceClient.ApproveEnrollmentRequestWithResponse(
			ctx,
			enrollmentId,
			v1alpha1.EnrollmentRequestApproval{
				Approved: true,
				Labels:   labels,
			})
		if err != nil {
			log.Errorf("Error approving device %s enrollment: %v", enrollmentId, err)
			return false, nil
		}
		if resp.HTTPResponse != nil {
			_ = resp.HTTPResponse.Body.Close()
		}
		log.Infof("Approved device enrollment %s", enrollmentId)
		return true, nil
	})
	if err != nil && ctx.Err() == nil {
		log.Fatalf("Error approving device enrollment: %v", err)
	}
}

func readBannerFile(agentDir string) (string, error) {
	var data []byte
	var err error
	bannerFile := filepath.Join(agentDir, lifecycle.BannerFile)
	if _, err = os.Stat(bannerFile); err != nil {
		return "", err
	}
	data, err = os.ReadFile(bannerFile)
	if err != nil {
		return "", err
	}
	return string(data), nil
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

func formatLabels(labelArgs *[]string) *map[string]string {
	formattedLabels := map[string]string{}

	if labelArgs != nil {
		formattedLabels = util.LabelArrayToMap(*labelArgs)
	}

	formattedLabels["created_by"] = "device-simulator"
	return &formattedLabels
}