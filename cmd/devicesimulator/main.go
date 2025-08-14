package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/lifecycle"
	apiClient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/experimental"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"golang.org/x/sync/semaphore"
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

func defaultConfigFilePath() string {
	return filepath.Join(util.MustString(os.UserHomeDir), "."+appName, "agent.yaml")
}

func defaultDataDir() string {
	return filepath.Join(util.MustString(os.UserHomeDir), "."+appName, "data")
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	fmt.Println("\nPositional commands:")
	fmt.Println("  version          Print device simulator version information")
	fmt.Println("  help             Show this help message")
	fmt.Println("\nThis program starts a device simulator with the specified configuration. Below are the available flags:")
	pflag.PrintDefaults()
}

func main() {
	log := flightlog.InitLogs()

	configFile := pflag.String("config", defaultConfigFilePath(), "path of the agent configuration template")
	dataDir := pflag.String("data-dir", defaultDataDir(), "directory for storing simulator data")
	labels := pflag.StringArray("label", []string{}, "label applied to simulated devices, in the format key=value")
	numDevices := pflag.Int("count", 1, "number of devices to simulate")
	initialDeviceIndex := pflag.Int("initial-device-index", 0, "starting index for device name suffix, (e.g., device-0000 for 0, device-0200 for 200))")
	metricsAddr := pflag.String("metrics", "localhost:9093", "address for the metrics endpoint")
	stopAfter := pflag.Duration("stop-after", 0, "stop the simulator after the specified duration")
	sourceIPs := pflag.StringSlice("source-ips", []string{}, "comma-separated list of source IP addresses for device management HTTP connections")
	maxConcurrency := pflag.Int("max-concurrency", 100, "maximum number of concurrent agent operations")
	agentStartupJitter := pflag.Duration("agent-startup-jitter", -1*time.Second, "maximum random delay when starting agents (negative = use status-update-interval, 0 = no jitter, positive = custom duration)")
	versionFormat := pflag.StringP("output", "o", "", fmt.Sprintf("Output format. One of: (%s). Default: text format", strings.Join(outputTypes, ", ")))
	logLevel := pflag.StringP("log-level", "v", "debug", "logger verbosity level (one of \"fatal\", \"error\", \"warn\", \"warning\", \"info\", \"debug\")")

	pflag.Usage = printUsage

	// Parse flags
	pflag.Parse()

	// Handle positional arguments
	args := pflag.Args()
	if len(args) > 0 {
		switch args[0] {
		case "help":
			printUsage()
			os.Exit(0)
		case "version":
			if err := reportVersion(versionFormat); err != nil {
				fmt.Println(err.Error())
				os.Exit(1)
			}
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", args[0])
			printUsage()
			os.Exit(1)
		}
	}

	logLvl, err := logrus.ParseLevel(*logLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid log level: %s\n\n", *logLevel)
		printUsage()
		os.Exit(1)
	}
	log.SetLevel(logLvl)

	// Parse and validate source IPs
	var parsedSourceIPs []net.IP
	for _, ipStr := range *sourceIPs {
		if ip := net.ParseIP(ipStr); ip != nil {
			parsedSourceIPs = append(parsedSourceIPs, ip)
			log.Infof("Using source IP: %s", ip.String())
		} else {
			log.Fatalf("Invalid source IP address: %s", ipStr)
		}
	}

	log.Infoln("command line flags:")
	pflag.CommandLine.VisitAll(func(flg *pflag.Flag) {
		log.Infof("  %s=%s", flg.Name, flg.Value)
	})

	formattedLables := formatLabels(labels)

	agentConfigTemplate := createAgentConfigTemplate(*dataDir, *configFile, logLvl.String())

	log.Infoln("starting device simulator")
	defer log.Infoln("device simulator stopped")

	log.Infoln("setting up metrics endpoint")
	setupMetricsEndpoint(*metricsAddr)

	baseDir, err := client.DefaultFlightctlClientConfigPath()
	if err != nil {
		log.Fatalf("could not get user config directory: %v", err)
	}
	cfg, err := client.ParseConfigFile(baseDir)
	if err != nil {
		log.Fatalf("could not parse config file: %v", err)
	}
	// allow many idle conns to prevent tearing down connections we may need again
	cfg.AddHTTPOptions(client.WithMaxIdleConnsPerHost(*maxConcurrency))
	serviceClient, err := client.NewFromConfig(cfg, baseDir)
	if err != nil {
		log.Fatalf("Error creating service client: %v", err)
	}

	log.Infoln("creating agents")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create simulator fleet configuration
	if err := createSimulatorFleet(ctx, serviceClient, log); err != nil {
		log.Warnf("Failed to create simulator fleet: %v", err)
	}

	agents, agentsFolders := createAgents(createAgentsConfig{
		log:                 log,
		numDevices:          *numDevices,
		initialDeviceIndex:  *initialDeviceIndex,
		agentConfigTemplate: agentConfigTemplate,
		parsedSourceIPs:     parsedSourceIPs,
		maxConcurrency:      *maxConcurrency,
	})

	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigShutdown
		signal.Stop(sigShutdown)
		log.Printf("Shutdown signal received (%v).", sig)
		cancel()
	}()

	log.Infoln("running agents")
	// limit the maximum number of devices that are being approved.
	sem := semaphore.NewWeighted(int64(*maxConcurrency))

	// default to using the agent configuration's StatusUpdateInterval
	jitterDuration := *agentStartupJitter
	if *agentStartupJitter < 0 {
		jitterDuration = time.Duration(agentConfigTemplate.StatusUpdateInterval)
	}

	launchParams := agentLaunchParams{
		agents:          agents,
		agentFolders:    agentsFolders,
		log:             log,
		serviceClient:   serviceClient,
		formattedLabels: formattedLables,
		sem:             sem,
		jitterDuration:  jitterDuration,
	}
	for i := range *numDevices {
		if err := sem.Acquire(ctx, 1); err != nil {
			break
		}
		go launchAgent(ctx, i, launchParams)
	}
	// block until we can acquire all entries. This is an indication that all devices have been
	// enrolled, and it's safe to start the "stopAfter" function.
	_ = sem.Acquire(ctx, int64(*maxConcurrency))
	if stopAfter != nil && *stopAfter > 0 {
		time.AfterFunc(*stopAfter, func() {
			log.Infoln("stopping simulator after duration")
			cancel()
		})
	}

	<-ctx.Done()
	log.Infoln("Simulator stopped.")
}

func launchAgent(ctx context.Context, i int, params agentLaunchParams) {
	defer params.sem.Release(1)
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(rand.Float64() * float64(params.jitterDuration))): //nolint:gosec
	}
	// leave the agent process running in the background
	// when the agent is approved, we return and release the semaphore to allow other agents to onboard
	go startAgent(ctx, params.agents[i], params.log, i)
	approveAgent(ctx, params.log, params.serviceClient, params.agentFolders[i], params.formattedLabels)
}

func reportVersion(versionFormat *string) error {
	cliVersion := version.Get()
	switch *versionFormat {
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
		return fmt.Errorf("VersionOptions were not validated: --output=%q should have been rejected\n", *versionFormat)
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

func createAgentConfigTemplate(dataDir string, configFile string, logLevelOverride string) *agent_config.Config {
	agentConfigTemplate := agent_config.NewDefault()
	agentConfigTemplate.ConfigDir = filepath.Dir(configFile)
	if err := agentConfigTemplate.ParseConfigFile(configFile); err != nil {
		log.Fatalf("Error parsing config: %v", err)
	}

	var tmpConfig agent_config.Config
	fileBytes, err := os.ReadFile(configFile)
	if err != nil {
		log.Fatalf("Error parsing config: %v", err)
	}

	err = yaml.Unmarshal(fileBytes, &tmpConfig)
	if err != nil {
		log.Fatalf("Error parsing config: %v", err)
	}

	if tmpConfig.LogLevel == "" {
		agentConfigTemplate.LogLevel = logLevelOverride
	}

	// create data directory if not exists
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

func copyAgentFiles(log *logrus.Logger, certDir, agentDir string) {
	for _, filename := range []string{"ca.crt", "client-enrollment.crt", "client-enrollment.key"} {
		if err := copyFile(filepath.Join(certDir, filename), filepath.Join(agentDir, agent_config.DefaultConfigDir, filename)); err != nil {
			log.Fatalf("copying %s: %v", filename, err)
		}
	}
}

type createAgentsConfig struct {
	log                 *logrus.Logger
	numDevices          int
	initialDeviceIndex  int
	agentConfigTemplate *agent_config.Config
	parsedSourceIPs     []net.IP
	maxConcurrency      int
}

type agentLaunchParams struct {
	agents          []*agent.Agent
	agentFolders    []string
	log             *logrus.Logger
	serviceClient   *apiClient.ClientWithResponses
	formattedLabels *map[string]string
	sem             *semaphore.Weighted
	jitterDuration  time.Duration
}

func createAgents(agentCfg createAgentsConfig) ([]*agent.Agent, []string) {
	logger := agentCfg.log
	logger.Infoln("creating agents")
	agents := make([]*agent.Agent, agentCfg.numDevices)
	agentsFolders := make([]string, agentCfg.numDevices)
	ex := experimental.NewFeatures()
	if ex.IsEnabled() && agentCfg.numDevices > 1 {
		logger.Warnf("Using experimental features with more than one device could cause unexpected issues.")
	}

	enrollmentTransport := client.WithCachedTransport()

	for i := 0; i < agentCfg.numDevices; i++ {
		agentName := fmt.Sprintf("device-%05d", agentCfg.initialDeviceIndex+i)
		certDir := filepath.Join(agentCfg.agentConfigTemplate.ConfigDir, "certs")
		agentDir := filepath.Join(agentCfg.agentConfigTemplate.DataDir, agentName)
		// Cleanup if exists and initialize the agent's expected
		os.RemoveAll(agentDir)
		if err := os.MkdirAll(filepath.Join(agentDir, agent_config.DefaultConfigDir), 0700); err != nil {
			logger.Fatalf("Error creating directory: %v", err)
		}

		if ex.IsEnabled() {
			setupTPMLinks(agentDir, logger)
		}

		err := os.Setenv(client.TestRootDirEnvKey, agentDir)
		if err != nil {
			logger.Fatalf("Error setting environment variable: %v", err)
		}

		// Disable console banner for simulated agents
		err = os.Setenv("FLIGHTCTL_DISABLE_CONSOLE_BANNER", "true")
		if err != nil {
			logger.Fatalf("Error setting banner disable environment variable: %v", err)
		}

		// Enable simulation mode for agents
		err = os.Setenv("FLIGHTCTL_SIMULATED", "true")
		if err != nil {
			logger.Fatalf("Error setting simulation mode environment variable: %v", err)
		}

		copyAgentFiles(logger, certDir, agentDir)

		cfg := agent_config.NewDefault()
		cfg.DefaultLabels["alias"] = agentName
		cfg.ConfigDir = agent_config.DefaultConfigDir
		cfg.DataDir = agent_config.DefaultConfigDir
		cfg.EnrollmentService = config.EnrollmentService{}
		cfg.EnrollmentService.Config = *client.NewDefault()
		cfg.EnrollmentService.Config.Service = client.Service{
			Server:               agentCfg.agentConfigTemplate.EnrollmentService.Config.Service.Server,
			CertificateAuthority: filepath.Join(cfg.ConfigDir, agent_config.CacertFile),
		}
		cfg.EnrollmentService.Config.AuthInfo = client.AuthInfo{
			ClientCertificate: filepath.Join(cfg.ConfigDir, agent_config.EnrollmentCertFile),
			ClientKey:         filepath.Join(cfg.ConfigDir, agent_config.EnrollmentKeyFile),
		}
		cfg.SpecFetchInterval = agentCfg.agentConfigTemplate.SpecFetchInterval
		cfg.StatusUpdateInterval = agentCfg.agentConfigTemplate.StatusUpdateInterval
		cfg.TPM = agentCfg.agentConfigTemplate.TPM
		cfg.LogPrefix = agentName

		// create managementService config
		cfg.ManagementService = config.ManagementService{}
		cfg.ManagementService.Config = *client.NewDefault()
		cfg.ManagementService.Service = client.Service{
			Server:               agentCfg.agentConfigTemplate.ManagementService.Config.Service.Server,
			CertificateAuthority: filepath.Join(cfg.ConfigDir, agent_config.CacertFile),
		}
		cfg.SystemInfo = []string{}

		cfg.SetEnrollmentMetricsCallback(rpcMetricsCallback)

		// Device management currently requires setting up individual mTLS connections. This makes HTTP connection reuse
		// effectively impossible across agents. When a device is onboarded, a new http connection must be generated.
		// To avoid ephemeral port exhaustion, we allow the management client to bind to specific IPs.
		if len(agentCfg.parsedSourceIPs) > 0 {
			// Assign source IP if provided (round-robin distribution)
			sourceIP := agentCfg.parsedSourceIPs[i%len(agentCfg.parsedSourceIPs)]
			// Create dialer with source IP, using same defaults as the default http dialer (30s timeout/keepalive)
			cfg.ManagementService.Config.AddHTTPOptions(baseclient.WithDialer(&net.Dialer{
				LocalAddr: &net.TCPAddr{IP: sourceIP},
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}))
			logger.Infof("Agent %s assigned source IP: %s", agentName, sourceIP.String())
		}
		// The enrollment client configuration is the same for all agents and thus no need to create a new connection for
		// each agent. By using the CachedTransport option, we let the agents setup their enrollment clients (and
		// individual transports), and then swap out the transport such that all HTTP clients are using the same transport.
		// This allows for connection reuse across all agents during the enrollment phase.
		// Additionally, the number of connections required is limited to the concurrency level defined.
		// As the expected concurrency level is relatively low (compared to the total number of running agents), we're
		// not as concerned with ephemeral port exhaustion and can just use the default option of letting the OS choose.
		cfg.EnrollmentService.Config.AddHTTPOptions(
			baseclient.WithMaxIdleConnsPerHost(agentCfg.maxConcurrency),
			enrollmentTransport,
		)

		if err := cfg.Complete(); err != nil {
			logger.Fatalf("agent config %d: %v", i, err)
		}
		if err := cfg.Validate(); err != nil {
			logger.Fatalf("agent config %d: %v", i, err)
		}

		logWithPrefix := flightlog.NewPrefixLogger(agentName)
		logWithPrefix.Level(agentCfg.agentConfigTemplate.LogLevel)
		agents[i] = agent.New(logWithPrefix, cfg, "")
		agentsFolders[i] = agentDir
	}
	return agents, agentsFolders
}

func approveAgent(ctx context.Context, log *logrus.Logger, serviceClient *apiClient.ClientWithResponses, agentDir string, labels *map[string]string) {
	enrollmentId := ""
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, false, func(ctx context.Context) (bool, error) {
		log.Infof("Approving device enrollment if exists for agent %s", filepath.Base(agentDir))
		if enrollmentId == "" {
			bannerFileData, err := readBannerFile(agentDir)
			if err != nil {
				log.Warnf("Error reading banner file: %v", err)
				return false, nil
			}
			enrollmentId = testutil.GetEnrollmentIdFromText(bannerFileData)
			if enrollmentId == "" {
				log.Warnf("No enrollment id found in banner file %s", bannerFileData)
				return false, nil
			}
		}
		// timeout after 30s and retry
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
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
		responseCode := resp.StatusCode()
		if responseCode == http.StatusNotFound {
			// no error, but don't log this. There could be a race condition in posting vs approving and is not exceptional
			return false, nil
		}
		if responseCode < http.StatusOK || responseCode >= http.StatusMultipleChoices {
			log.Warnf("Failed approving device %s enrollment: %d", enrollmentId, responseCode)
			return false, nil
		}
		log.Infof("Approved device enrollment %s", enrollmentId)
		return true, nil
	})
	if err != nil && ctx.Err() == nil {
		log.Errorf("Error approving device enrollment: %v", err)
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

func formatLabels(lableArgs *[]string) *map[string]string {
	formattedLabels := map[string]string{}

	if lableArgs != nil {
		formattedLabels = util.LabelArrayToMap(*lableArgs)
	}

	formattedLabels["created_by"] = "device-simulator"
	return &formattedLabels
}

func setupTPMLinks(agentDir string, log *logrus.Logger) {
	// Create /dev directory in agent dir
	devDir := filepath.Join(agentDir, "dev")
	if err := os.MkdirAll(devDir, 0700); err != nil {
		log.Warnf("Failed to create /dev directory for TPM links: %v", err)
		return
	}

	// Create /sys/class/tpm directory in agent dir
	sysTPMDir := filepath.Join(agentDir, "sys", "class", "tpm")
	if err := os.MkdirAll(sysTPMDir, 0700); err != nil {
		log.Warnf("Failed to create /sys/class/tpm directory for TPM links: %v", err)
		return
	}

	// Check if host has TPM devices by looking for /sys/class/tpm
	hostTPMDir := "/sys/class/tpm"
	if _, err := os.Stat(hostTPMDir); os.IsNotExist(err) {
		log.Infof("No TPM devices found on host, skipping TPM link setup")
		return
	}

	// Read TPM devices from host
	entries, err := os.ReadDir(hostTPMDir)
	if err != nil {
		log.Warnf("Failed to read TPM devices from host: %v", err)
		return
	}

	for _, entry := range entries {
		// skip tpmrm entries but keep the tpm entries
		if !strings.HasPrefix(entry.Name(), "tpm") || strings.HasPrefix(entry.Name(), "tpmrm") {
			continue
		}
		log.Infof("Linking tpm device %s", entry.Name())

		deviceNum := strings.TrimPrefix(entry.Name(), "tpm")

		// Create symlinks for device files
		hostDevicePath := fmt.Sprintf("/dev/tpm%s", deviceNum)
		hostResourceMgrPath := fmt.Sprintf("/dev/tpmrm%s", deviceNum)
		agentDevicePath := filepath.Join(devDir, fmt.Sprintf("tpm%s", deviceNum))
		agentResourceMgrPath := filepath.Join(devDir, fmt.Sprintf("tpmrm%s", deviceNum))

		// Only create symlinks if the host device files exist
		if _, err := os.Stat(hostDevicePath); err == nil {
			if err := os.Symlink(hostDevicePath, agentDevicePath); err != nil {
				log.Warnf("Failed to create symlink %s -> %s: %v", agentDevicePath, hostDevicePath, err)
			}
		}

		if _, err := os.Stat(hostResourceMgrPath); err == nil {
			if err := os.Symlink(hostResourceMgrPath, agentResourceMgrPath); err != nil {
				log.Warnf("Failed to create symlink %s -> %s: %v", agentResourceMgrPath, hostResourceMgrPath, err)
			}
		}

		// Create symlink for sysfs directory
		hostSysfsPath := filepath.Join(hostTPMDir, entry.Name())
		agentSysfsPath := filepath.Join(sysTPMDir, entry.Name())
		if err := os.Symlink(hostSysfsPath, agentSysfsPath); err != nil {
			log.Warnf("Failed to create symlink %s -> %s: %v", agentSysfsPath, hostSysfsPath, err)
		}
	}
}

func createSimulatorFleet(ctx context.Context, serviceClient *apiClient.ClientWithResponses, log *logrus.Logger) error {
	fleetName := "simulator-disk-monitoring"

	// Check if fleet already exists
	response, err := serviceClient.GetFleetWithResponse(ctx, fleetName, &v1alpha1.GetFleetParams{})
	if err == nil && response.HTTPResponse != nil && response.HTTPResponse.StatusCode == 200 {
		log.Infof("Fleet %s already exists, skipping creation", fleetName)
		return nil
	}

	log.Infof("Creating fleet configuration: %s", fleetName)

	// Load fleet configuration from YAML file
	fleetYAMLPath := filepath.Join("examples", "fleet-disk-simulator.yaml")
	fleetYAMLData, err := os.ReadFile(fleetYAMLPath)
	if err != nil {
		return fmt.Errorf("reading fleet YAML file %s: %w", fleetYAMLPath, err)
	}

	var fleet v1alpha1.Fleet
	if err := yaml.Unmarshal(fleetYAMLData, &fleet); err != nil {
		return fmt.Errorf("unmarshaling fleet YAML: %w", err)
	}

	// Convert to JSON
	fleetJSON, err := json.Marshal(fleet)
	if err != nil {
		return fmt.Errorf("marshaling fleet configuration: %w", err)
	}

	// Create the fleet
	createResponse, err := serviceClient.ReplaceFleetWithBodyWithResponse(ctx, fleetName, "application/json", bytes.NewReader(fleetJSON))
	if err != nil {
		return fmt.Errorf("creating fleet: %w", err)
	}

	if createResponse.HTTPResponse != nil && createResponse.HTTPResponse.StatusCode >= 200 && createResponse.HTTPResponse.StatusCode < 300 {
		log.Infof("Successfully created fleet: %s", fleetName)
		return nil
	}

	return fmt.Errorf("failed to create fleet: status %d, body: %s", createResponse.HTTPResponse.StatusCode, string(createResponse.Body))
}
