package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
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

	enrollmentPollInterval = 500 * time.Millisecond
	enrollmentTimeout      = 5 * time.Minute
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
	configFile := pflag.String("config", defaultConfigFilePath(), "path of the agent configuration template")
	dataDir := pflag.String("data-dir", defaultDataDir(), "directory for storing simulator data")
	labels := pflag.StringArray("label", []string{}, "label applied to simulated devices, in the format key=value")
	numDevices := pflag.Int("count", 1, "number of devices to simulate")
	initialDeviceIndex := pflag.Int("initial-device-index", 0, "starting index for device name suffix, (e.g., device-0000 for 0, device-0200 for 200))")
	metricsAddr := pflag.String("metrics", "localhost:9093", "address for the metrics endpoint")
	stopAfter := pflag.Duration("stop-after", 0, "stop the simulator after the specified duration")
	sourceIPs := pflag.StringSlice("source-ips", []string{}, "comma-separated list of existing source IP addresses for device management HTTP connections (mutually exclusive with --source-ip-base/--source-ip-count)")
	setupSourceIPsFlag := pflag.Bool("setup-source-ips", false, "standalone root-capable mode: create source IP aliases on an interface, then exit")
	teardownSourceIPsFlag := pflag.Bool("teardown-source-ips", false, "standalone root-capable mode: remove source IP aliases created for scale tests, then exit")
	sourceIPIface := pflag.String("source-ip-iface", "", "interface for --setup-source-ips/--teardown-source-ips (optional on normal runs)")
	sourceIPBase := pflag.String("source-ip-base", "", "first IPv4 in a consecutive source-IP range (used by setup/teardown and non-root runs)")
	sourceIPCount := pflag.Int("source-ip-count", 0, "number of consecutive IPv4 addresses in the source-IP range")
	sourceIPPrefix := pflag.Int("source-ip-prefix", 24, "prefix length for --setup-source-ips/--teardown-source-ips")
	maxConcurrency := pflag.Int("max-concurrency", 200, "maximum number of concurrent agent create/enroll operations")
	agentStartupJitter := pflag.Duration("agent-startup-jitter", 0, "maximum random delay when starting agents (negative = use status-update-interval, 0 = no jitter, positive = custom duration)")
	skipAutoApprove := pflag.Bool("skip-auto-approve", false, "do not auto-approve enrollment requests (agents wait for manual approval)")
	clean := pflag.Bool("clean", false, "wipe local simulator state and delete simulator-created devices/enrollment requests before starting")
	cleanOnly := pflag.Bool("clean-only", false, "wipe local simulator state and delete simulator-created devices/enrollment requests, then exit")
	fleetCount := pflag.Int("fleet-count", 0, "number of scale fleets to create and distribute devices across (0 disables)")
	fleetPrefix := pflag.String("fleet-prefix", "scale-fleet", "prefix for scale fleet names (<prefix>-NN)")
	rollout := pflag.Bool("rollout", false, "standalone mode: update fleet templates and measure rollout convergence, then exit")
	rolloutTemplate := pflag.String("rollout-template", "", "path to a Fleet YAML whose .spec.template is applied during --rollout")
	rolloutTimeout := pflag.Duration("rollout-timeout", 15*time.Minute, "how long --rollout waits for devices to become UpToDate")
	versionFormat := pflag.StringP("output", "o", "", fmt.Sprintf("Output format. One of: (%s). Default: text format", strings.Join(outputTypes, ", ")))
	logLevel := pflag.StringP("log-level", "v", "error", "log level for simulated device agents only (one of \"fatal\", \"error\", \"warn\", \"warning\", \"info\", \"debug\")")
	orchestratorLogLevel := pflag.String("orchestrator-log-level", "info", "log level for devicesimulator orchestration (cleanup, fleets, enrollment progress)")

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

	if _, err := logrus.ParseLevel(*logLevel); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid device log level: %s\n\n", *logLevel)
		printUsage()
		os.Exit(1)
	}
	if _, err := logrus.ParseLevel(*orchestratorLogLevel); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid orchestrator log level: %s\n\n", *orchestratorLogLevel)
		printUsage()
		os.Exit(1)
	}

	log := flightlog.InitLogs(*orchestratorLogLevel)

	if *setupSourceIPsFlag && *teardownSourceIPsFlag {
		log.Fatalf("--setup-source-ips and --teardown-source-ips are mutually exclusive")
	}
	if (*setupSourceIPsFlag || *teardownSourceIPsFlag) && len(*sourceIPs) > 0 {
		log.Fatalf("--source-ips is mutually exclusive with --setup-source-ips/--teardown-source-ips")
	}
	if (*setupSourceIPsFlag || *teardownSourceIPsFlag) && (*clean || *cleanOnly || *rollout) {
		log.Fatalf("--setup-source-ips/--teardown-source-ips are mutually exclusive with --clean/--clean-only/--rollout")
	}
	if *rollout && (*clean || *cleanOnly) {
		log.Fatalf("--rollout is mutually exclusive with --clean/--clean-only")
	}
	if *rollout && *rolloutTemplate == "" {
		log.Fatalf("--rollout requires --rollout-template")
	}
	if *rollout && *fleetCount <= 0 {
		log.Fatalf("--rollout requires --fleet-count > 0")
	}

	log.Infoln("command line flags:")
	pflag.CommandLine.VisitAll(func(flg *pflag.Flag) {
		log.Infof("  %s=%s", flg.Name, flg.Value)
	})

	if *setupSourceIPsFlag {
		ips, err := setupSourceIPs(log, *sourceIPIface, *sourceIPBase, *sourceIPCount, *sourceIPPrefix)
		if err != nil {
			log.Fatalf("source IP setup failed: %v", err)
		}
		log.Infof("source IPs ready; run the simulator as a non-root user with the same --source-ip-base/--source-ip-count (or --source-ips=%s)", sourceIPsFlagValue(ips))
		return
	}
	if *teardownSourceIPsFlag {
		if err := teardownSourceIPs(log, *sourceIPIface, *sourceIPBase, *sourceIPCount, *sourceIPPrefix); err != nil {
			log.Fatalf("source IP teardown failed: %v", err)
		}
		log.Infoln("source IP teardown complete")
		return
	}

	// Disable console banner for all simulated agents
	if err := os.Setenv("FLIGHTCTL_DISABLE_CONSOLE_BANNER", "true"); err != nil {
		log.Fatalf("Error setting banner disable environment variable: %v", err)
	}

	// Bind to existing addresses only; no privileges required. Missing IPs fail at dial/bind time.
	parsedSourceIPs, err := resolveRuntimeSourceIPs(log, *sourceIPs, *sourceIPIface, *sourceIPBase, *sourceIPCount, *sourceIPPrefix)
	if err != nil {
		log.Fatalf("%v", err)
	}

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
		if errors.Is(err, os.ErrNotExist) {
			log.Fatalf("no client config found at %s — run 'flightctl login' first", baseDir)
		}
		log.Fatalf("could not parse config file %s: %v", baseDir, err)
	}
	if cfg.Organization != "" {
		log.Infof("using organization %s from client config", cfg.Organization)
	} else {
		log.Infoln("no organization set in client config")
	}
	// allow many idle conns to prevent tearing down connections we may need again
	cfg.AddHTTPOptions(client.WithMaxIdleConnsPerHost(*maxConcurrency))
	serviceClient, err := client.NewFromConfig(cfg, baseDir)
	if err != nil {
		log.Fatalf("Error creating service client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serviceClient.Start(ctx)
	defer serviceClient.Stop()

	if *clean || *cleanOnly {
		if err := cleanSimulatorState(ctx, log, serviceClient.ClientWithResponses, *dataDir); err != nil {
			log.Fatalf("cleanup failed: %v", err)
		}
		if *cleanOnly {
			log.Infoln("clean-only complete, exiting")
			return
		}
	}

	if *rollout {
		if err := runRollout(ctx, log, serviceClient.ClientWithResponses, *fleetPrefix, *fleetCount, *rolloutTemplate, *rolloutTimeout); err != nil {
			log.Fatalf("rollout failed: %v", err)
		}
		return
	}

	formattedLables := formatLabels(labels)
	agentConfigTemplate := createAgentConfigTemplate(*dataDir, *configFile, *logLevel)

	var fleetNames []string
	if *fleetCount > 0 {
		log.Infof("skipping default simulator-disk-monitoring fleet because --fleet-count=%d", *fleetCount)
		var err error
		fleetNames, err = createScaleFleets(ctx, log, serviceClient.ClientWithResponses, *fleetPrefix, *fleetCount)
		if err != nil {
			log.Fatalf("Failed to create scale fleets: %v", err)
		}
	} else if err := createSimulatorFleet(ctx, serviceClient.ClientWithResponses, log); err != nil {
		log.Warnf("Failed to create simulator fleet: %v", err)
	}

	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigShutdown
		signal.Stop(sigShutdown)
		log.Printf("Shutdown signal received (%v).", sig)
		cancel()
	}()

	log.Infof("starting %d agents (concurrency=%d, jitter=%s)", *numDevices, *maxConcurrency, agentStartupJitter.String())
	sem := semaphore.NewWeighted(int64(*maxConcurrency))

	jitterDuration := *agentStartupJitter
	if *agentStartupJitter < 0 {
		jitterDuration = time.Duration(agentConfigTemplate.StatusUpdateInterval)
	}

	createCfg := createAgentsConfig{
		log:                 log,
		numDevices:          *numDevices,
		initialDeviceIndex:  *initialDeviceIndex,
		agentConfigTemplate: agentConfigTemplate,
		parsedSourceIPs:     parsedSourceIPs,
		maxConcurrency:      *maxConcurrency,
		simulatorLabels:     formattedLables,
		fleetNames:          fleetNames,
		enrollmentTransport: client.WithCachedTransport(),
	}
	var createMu sync.Mutex
	var enrolled atomic.Int64
	started := time.Now()

	launchParams := agentLaunchParams{
		createCfg:       createCfg,
		createMu:        &createMu,
		log:             log,
		serviceClient:   serviceClient.ClientWithResponses,
		sem:             sem,
		jitterDuration:  jitterDuration,
		skipAutoApprove: *skipAutoApprove,
		enrolled:        &enrolled,
		total:           *numDevices,
		started:         started,
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
	log.Infof("all %d agents enrolled in %s (%.1f devices/s)", *numDevices, time.Since(started).Round(time.Second), float64(*numDevices)/time.Since(started).Seconds())
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
	if params.jitterDuration > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(rand.Float64() * float64(params.jitterDuration))): //nolint:gosec
		}
	}
	agentInstance, agentDir, labels, err := createOneAgent(params.createCfg, params.createMu, i)
	if err != nil {
		params.log.Errorf("creating agent %d: %v", i, err)
		recordEnrollmentOutcome(ctx, err)
		return
	}
	// leave the agent process running in the background
	// when the agent is approved, we return and release the semaphore to allow other agents to onboard
	go startAgent(ctx, agentInstance, params.log, i)
	if params.skipAutoApprove {
		waitForEnrollmentRequest(ctx, params.log, agentDir)
	} else {
		approveAgent(ctx, params.log, params.serviceClient, agentDir, labels)
	}
	done := params.enrolled.Add(1)
	if done%100 == 0 || done == int64(params.total) {
		elapsed := time.Since(params.started).Seconds()
		rate := float64(done) / elapsed
		params.log.Infof("enrollment progress: %d/%d (%.1f devices/s)", done, params.total, rate)
	}
}

func waitForEnrollmentRequest(ctx context.Context, log *logrus.Logger, agentDir string) {
	log.Debugf("Waiting for enrollment request for agent %s", filepath.Base(agentDir))
	enrollmentID, err := testutil.WaitForEnrollmentID(ctx, agentDir, enrollmentPollInterval, enrollmentTimeout)
	recordEnrollmentOutcome(ctx, err)
	if err != nil {
		if ctx.Err() == nil {
			log.Errorf("Error waiting for enrollment request: %v", err)
		}
		return
	}
	log.Debugf("Enrollment request visible for agent %s (id: %s)", filepath.Base(agentDir), enrollmentID)
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

func copyAgentFiles(certDir, agentDir string) error {
	for _, filename := range []string{"ca.crt", "client-enrollment.crt", "client-enrollment.key"} {
		if err := copyFile(filepath.Join(certDir, filename), filepath.Join(agentDir, agent_config.DefaultConfigDir, filename)); err != nil {
			return fmt.Errorf("copying %s: %w", filename, err)
		}
	}
	return nil
}

type createAgentsConfig struct {
	log                 *logrus.Logger
	numDevices          int
	initialDeviceIndex  int
	agentConfigTemplate *agent_config.Config
	parsedSourceIPs     []net.IP
	maxConcurrency      int
	simulatorLabels     *map[string]string
	fleetNames          []string
	enrollmentTransport baseclient.HTTPClientOption
}

type agentLaunchParams struct {
	createCfg       createAgentsConfig
	createMu        *sync.Mutex
	log             *logrus.Logger
	serviceClient   *apiClient.ClientWithResponses
	sem             *semaphore.Weighted
	jitterDuration  time.Duration
	skipAutoApprove bool
	enrolled        *atomic.Int64
	total           int
	started         time.Time
}

func createOneAgent(agentCfg createAgentsConfig, createMu *sync.Mutex, i int) (*agent.Agent, string, *map[string]string, error) {
	logger := agentCfg.log
	agentName := fmt.Sprintf("device-%05d", agentCfg.initialDeviceIndex+i)
	certDir := filepath.Join(agentCfg.agentConfigTemplate.ConfigDir, "certs")
	agentDir := filepath.Join(agentCfg.agentConfigTemplate.DataDir, agentName)

	_, err := os.Stat(agentDir)
	resuming := err == nil
	if resuming {
		logger.Debugf("resuming existing state for agent %s", agentName)
	} else {
		if err := os.MkdirAll(filepath.Join(agentDir, agent_config.DefaultConfigDir), 0700); err != nil {
			return nil, "", nil, fmt.Errorf("creating directory: %w", err)
		}
		if experimental.NewFeatures().IsEnabled() {
			setupTPMLinks(agentDir, logger)
		}
		if err := copyAgentFiles(certDir, agentDir); err != nil {
			return nil, "", nil, err
		}
	}

	labels := map[string]string{}
	if agentCfg.simulatorLabels != nil {
		for k, v := range *agentCfg.simulatorLabels {
			labels[k] = v
		}
	}
	if len(agentCfg.fleetNames) > 0 {
		labels["fleet"] = agentCfg.fleetNames[i%len(agentCfg.fleetNames)]
	}

	// FLIGHTCTL_TEST_ROOT_DIR is process-global; only Setenv + NewDefault must be serialized.
	createMu.Lock()
	if err := os.Setenv(client.TestRootDirEnvKey, agentDir); err != nil {
		createMu.Unlock()
		return nil, "", nil, fmt.Errorf("setting %s: %w", client.TestRootDirEnvKey, err)
	}
	cfg := agent_config.NewDefault()
	enrollmentCfg := client.NewDefault()
	managementCfg := client.NewDefault()
	createMu.Unlock()

	for k, v := range labels {
		cfg.DefaultLabels[k] = v
	}
	cfg.DefaultLabels["alias"] = agentName
	cfg.ConfigDir = agent_config.DefaultConfigDir
	cfg.DataDir = agent_config.DefaultConfigDir
	cfg.EnrollmentService = config.EnrollmentService{}
	cfg.EnrollmentService.Config = *enrollmentCfg
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

	cfg.ManagementService = config.ManagementService{}
	cfg.ManagementService.Config = *managementCfg
	cfg.ManagementService.Service = client.Service{
		Server:               agentCfg.agentConfigTemplate.ManagementService.Config.Service.Server,
		CertificateAuthority: filepath.Join(cfg.ConfigDir, agent_config.CacertFile),
	}
	cfg.SystemInfo = []string{}

	cfg.SetEnrollmentMetricsCallback(rpcMetricsCallback)

	if len(agentCfg.parsedSourceIPs) > 0 {
		sourceIP := agentCfg.parsedSourceIPs[i%len(agentCfg.parsedSourceIPs)]
		cfg.ManagementService.Config.AddHTTPOptions(baseclient.WithDialer(&net.Dialer{
			LocalAddr: &net.TCPAddr{IP: sourceIP},
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}))
		logger.Debugf("Agent %s assigned source IP: %s", agentName, sourceIP.String())
	}
	cfg.EnrollmentService.Config.AddHTTPOptions(
		baseclient.WithMaxIdleConnsPerHost(agentCfg.maxConcurrency),
		agentCfg.enrollmentTransport,
	)

	cfg.LogLevel = agentCfg.agentConfigTemplate.LogLevel
	agentInstance, err := testutil.NewSimulatedAgent(cfg, agentName, agent.WithExecuter(newSimulatorExecuter()))
	if err != nil {
		return nil, "", nil, fmt.Errorf("agent config %d: %w", i, err)
	}
	return agentInstance, agentDir, &labels, nil
}

func approveAgent(ctx context.Context, log *logrus.Logger, serviceClient *apiClient.ClientWithResponses, agentDir string, labels *map[string]string) {
	log.Debugf("Approving device enrollment if exists for agent %s", filepath.Base(agentDir))
	enrollmentID, err := testutil.ApproveEnrollment(ctx, serviceClient, agentDir, labels, enrollmentPollInterval, enrollmentTimeout)
	recordEnrollmentOutcome(ctx, err)
	if err != nil {
		if ctx.Err() == nil {
			log.Errorf("Error approving device enrollment: %v", err)
		}
		return
	}
	log.Debugf("Approved device enrollment %s", enrollmentID)
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

	formattedLabels["created_by"] = simulatorCreatedByValue
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
	response, err := serviceClient.GetFleetWithResponse(ctx, fleetName, &v1beta1.GetFleetParams{})
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

	var fleet v1beta1.Fleet
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

	if createResponse.StatusCode() >= 200 && createResponse.StatusCode() < 300 {
		log.Infof("Successfully created fleet: %s", fleetName)
		return nil
	}

	return fmt.Errorf("failed to create fleet: status %d, body: %s", createResponse.StatusCode(), string(createResponse.Body))
}
