package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
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

	// Default values for configuration
	defaultMaxConcurrency = 50
	defaultMaxRetries     = 3
	defaultRetryDelay     = 10 * time.Second
	defaultMetricsAddr    = "localhost:9093"
	defaultLogLevel       = "debug"

	// Device name formatting
	deviceNameFormat = "device-%05d"

	// Timeouts and intervals
	enrollmentCheckInterval = 5 * time.Second
	requestTimeout          = 5 * time.Second
)

var (
	outputTypes = []string{jsonFormat, yamlFormat}
)

type agentInstance struct {
	agent  *agent.Agent
	folder string
}

type deviceCreationConfig struct {
	// Agent configuration and management
	agents              []agentInstance
	agentConfigTemplate *agent_config.Config
	initialDeviceIndex  int
	formattedLabels     *map[string]string

	// Services and clients
	serviceClient *apiClient.ClientWithResponses
	log           *logrus.Logger

	// Concurrency control and retry configuration
	sem        *semaphore.Weighted
	wg         *sync.WaitGroup
	maxRetries int
	retryDelay time.Duration
}

func (c *deviceCreationConfig) agent(i int) *agent.Agent {
	return c.agents[i].agent
}

func (c *deviceCreationConfig) agentFolder(i int) string {
	return c.agents[i].folder
}

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
	fmt.Println("\nThis program starts a device simulator with the specified configuration.")
	fmt.Println("Available flags are organized into logical groups:")
	fmt.Println()

	// Print flags in logical groups
	printFlagGroup("Device Configuration", []string{"count", "initial-device-index", "label"})
	printFlagGroup("Concurrency and Retry Configuration", []string{"max-concurrency", "max-retries", "retry-delay"})
	printFlagGroup("Runtime Configuration", []string{"stop-after", "log-level", "metrics"})
	printFlagGroup("File and Directory Configuration", []string{"config", "data-dir"})
	printFlagGroup("Output Configuration", []string{"output"})
}

func printFlagGroup(groupName string, flagNames []string) {
	fmt.Printf("%s:\n", groupName)
	for _, flagName := range flagNames {
		if flag := pflag.Lookup(flagName); flag != nil {
			fmt.Printf("  -%s", flag.Shorthand)
			if flag.Shorthand != "" {
				fmt.Printf(", ")
			}
			fmt.Printf("--%s", flag.Name)
			if flag.Value.Type() != "bool" {
				fmt.Printf(" %s", flag.Value.Type())
			}
			fmt.Printf("\n        %s", flag.Usage)
			if flag.DefValue != "" {
				fmt.Printf(" (default %s)", flag.DefValue)
			}
			fmt.Println()
		}
	}
	fmt.Println()
}

func validateFlags(logLevel *string, maxRetries *int, numDevices *int, maxConcurrentAgents *int64, initialDeviceIndex *int) (*logrus.Logger, error) {
	log := flightlog.InitLogs()

	logLvl, err := logrus.ParseLevel(*logLevel)
	if err != nil {
		return nil, fmt.Errorf("invalid log level: %s", *logLevel)
	}
	log.SetLevel(logLvl)

	if *maxRetries < 0 {
		return nil, fmt.Errorf("maximum retries cannot be negative (got %d)", *maxRetries)
	}

	if *numDevices <= 0 {
		return nil, fmt.Errorf("number of devices must be positive (got %d)", *numDevices)
	}

	if *maxConcurrentAgents <= 0 {
		return nil, fmt.Errorf("max concurrency must be positive (got %d)", *maxConcurrentAgents)
	}

	if *initialDeviceIndex < 0 {
		return nil, fmt.Errorf("initial device index cannot be negative (got %d)", *initialDeviceIndex)
	}

	return log, nil
}

func main() {
	os.Setenv("FLIGHTCTL_QUIET_MODE", "true")

	// Device Configuration
	numDevices := pflag.Int("count", 1, "number of devices to simulate")
	initialDeviceIndex := pflag.Int("initial-device-index", 0, "starting index for device name suffix, (e.g., device-0000 for 0, device-0200 for 200))")
	labels := pflag.StringArray("label", []string{}, "label applied to simulated devices, in the format key=value")

	// Concurrency and Retry Configuration
	maxConcurrentAgents := pflag.Int64("max-concurrency", defaultMaxConcurrency, "maximum number of agents that can be creating simultaneously")
	maxRetries := pflag.Int("max-retries", defaultMaxRetries, "maximum number of retry attempts for failed device creation")
	retryDelay := pflag.Duration("retry-delay", defaultRetryDelay, "base delay between retry attempts")

	// Runtime Configuration
	stopAfter := pflag.Duration("stop-after", 0, "stop the simulator after the specified duration")
	logLevel := pflag.StringP("log-level", "v", defaultLogLevel, "logger verbosity level (one of \"fatal\", \"error\", \"warn\", \"warning\", \"info\", \"debug\")")
	metricsAddr := pflag.String("metrics", defaultMetricsAddr, "address for the metrics endpoint")

	// File and Directory Configuration
	configFile := pflag.String("config", defaultConfigFilePath(), "path of the agent configuration template")
	dataDir := pflag.String("data-dir", defaultDataDir(), "directory for storing simulator data")

	// Output Configuration
	versionFormat := pflag.StringP("output", "o", "", fmt.Sprintf("Output format. One of: (%s). Default: text format", strings.Join(outputTypes, ", ")))

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

	// Validate flags
	log, err := validateFlags(logLevel, maxRetries, numDevices, maxConcurrentAgents, initialDeviceIndex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n\n", err.Error())
		printUsage()
		os.Exit(1)
	}

	log.Infoln("command line flags:")
	pflag.CommandLine.VisitAll(func(flg *pflag.Flag) {
		log.Infof("  %s=%s", flg.Name, flg.Value)
	})

	formattedLabels := formatLabels(labels)

	agentConfigTemplate := createAgentConfigTemplate(*dataDir, *configFile)
	agentConfigTemplate.LogLevel = *logLevel

	log.Infoln("starting device simulator")
	defer log.Infoln("device simulator stopped")

	log.Infoln("setting up metrics endpoint")
	setupMetricsEndpoint(*metricsAddr)

	baseDir, err := client.DefaultFlightctlClientConfigPath()
	if err != nil {
		log.Fatalf("could not get user config directory: %v", err)
	}
	serviceClient, err := client.NewFromConfigFile(baseDir)
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

	agents := createAgents(log, *numDevices, *initialDeviceIndex, agentConfigTemplate)

	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigShutdown
		signal.Stop(sigShutdown)
		log.Printf("Shutdown signal received (%v).", sig)
		cancel()
	}()

	log.Infoln("running agents")

	config := &deviceCreationConfig{
		agents:              agents,
		agentConfigTemplate: agentConfigTemplate,
		initialDeviceIndex:  *initialDeviceIndex,
		formattedLabels:     formattedLabels,

		serviceClient: serviceClient,
		log:           log,

		sem:        semaphore.NewWeighted(*maxConcurrentAgents),
		wg:         new(sync.WaitGroup),
		maxRetries: *maxRetries,
		retryDelay: *retryDelay,
	}

	for i := range *numDevices {
		if err := config.sem.Acquire(ctx, 1); err != nil {
			break
		}

		config.wg.Add(1)
		go createDeviceWithRetry(ctx, i, config)
	}
	// Wait for all device creation goroutines to complete by acquiring the full semaphore weight.
	// This ensures the stopAfter timer only starts once all devices have finished onboarding.
	if err := config.sem.Acquire(ctx, *maxConcurrentAgents); err != nil {
		log.Warnf("failed to wait for all agents to be onboarded: %v", err)
	}
	if stopAfter != nil && *stopAfter > 0 {
		timer := time.AfterFunc(*stopAfter, func() {
			log.Infoln("stopping simulator after duration")
			cancel()
		})
		defer timer.Stop()
	}

	// Wait for all running agent processes to cleanup their resources
	config.wg.Wait()
	log.Infoln("Simulator stopped.")
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

func createAgentConfigTemplate(dataDir string, configFile string) *agent_config.Config {
	agentConfigTemplate := agent_config.NewDefault()
	agentConfigTemplate.ConfigDir = filepath.Dir(configFile)
	if err := agentConfigTemplate.ParseConfigFile(configFile); err != nil {
		log.Fatalf("Error parsing config: %v", err)
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
	copyDir := func(path string) string {
		return filepath.Join(agentDir, path)
	}
	fileCopyMap := map[string]struct {
		dir   string
		files []string
	}{
		certDir: {dir: copyDir(agent_config.DefaultConfigDir), files: []string{"ca.crt", "client-enrollment.crt", "client-enrollment.key"}},
		// these files aren't strictly necessary as the agent will work without them, but if they are not provided
		// the agent will spin for 45 seconds waiting for a default route to populate - an event that never occurs without
		// these files
		"/proc/net": {dir: copyDir("/proc/net"), files: []string{"route", "ipv6_route"}},
	}
	for dir, info := range fileCopyMap {
		for _, filename := range info.files {
			if err := copyFile(log, filepath.Join(dir, filename), filepath.Join(info.dir, filename)); err != nil {
				log.Fatalf("copying %s: %v", filename, err)
			}
		}
	}
}

func createAgents(log *logrus.Logger, numDevices int, initialDeviceIndex int, agentConfigTemplate *agent_config.Config) []agentInstance {
	log.Infoln("creating agents")
	agents := make([]agentInstance, numDevices)
	for i := 0; i < numDevices; i++ {
		agentName := fmt.Sprintf(deviceNameFormat, initialDeviceIndex+i)
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

		copyAgentFiles(log, certDir, agentDir)

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

		level, _ := logrus.ParseLevel(agentConfigTemplate.LogLevel)
		logWithPrefix := flightlog.NewPrefixLogger(agentName)
		logWithPrefix.SetLevel(level)
		agents[i] = agentInstance{
			agent:  agent.New(logWithPrefix, cfg, ""),
			folder: agentDir,
		}
	}
	return agents
}

func createDeviceWithRetry(ctx context.Context, deviceIndex int, config *deviceCreationConfig) {
	defer config.sem.Release(1)
	agentName := fmt.Sprintf(deviceNameFormat, config.initialDeviceIndex+deviceIndex)

	backoff := wait.Backoff{
		Duration: config.retryDelay,
		Factor:   2.0,
		Jitter:   0.1,
		Steps:    config.maxRetries + 1,
	}

	// wait for a random amount of time to add some initial jitter when creating many devices in parallel
	initialJitter := time.Duration(rand.Float64() * float64(config.agentConfigTemplate.StatusUpdateInterval)) //nolint:gosec
	select {
	case <-ctx.Done():
		config.wg.Done()
		return
	case <-time.After(initialJitter):
		break
	}

	var agentDoneChan <-chan struct{}
	attempt := 0
	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		if attempt > 0 {
			config.log.Infof("Retrying device creation for %s (attempt %d/%d)", agentName, attempt+1, config.maxRetries+1)
		}

		success, agentDone := createSingleDevice(ctx, deviceIndex, config)
		attempt++

		if success {
			agentDoneChan = agentDone
			return true, nil
		}
		return false, nil
	})

	if err != nil && !errors.Is(err, context.Canceled) {
		config.log.Errorf("Device creation for %s cancelled: %v", agentName, err)
	}
	if attempt > config.maxRetries {
		config.log.Errorf("Failed to create device %s after %d attempts", agentName, config.maxRetries+1)
	}

	// launch a background process to wait for the agent to finish it's cleanup when the context is canceled
	go func() {
		defer config.wg.Done()
		// If device creation succeeded, wait for the agent process to complete
		if agentDoneChan != nil {
			<-agentDoneChan
			config.log.Infof("Agent process for %s has completed", agentName)
		}
	}()
}

func startAgentProcess(ctx context.Context, deviceIndex int, config *deviceCreationConfig) {
	activeAgents.Inc()
	defer activeAgents.Dec()
	prefix := config.agent(deviceIndex).GetLogPrefix()

	err := config.agent(deviceIndex).Run(ctx)
	if err != nil {
		if wait.Interrupted(err) {
			config.log.Errorf("%s: agent timed out: %v", prefix, err)
		} else if ctx.Err() != nil {
			config.log.Infof("%s: agent stopped due to context cancellation.", prefix)
		} else {
			config.log.Errorf("%s: %v", prefix, err)
		}
	}
}

func approveAgentEnrollment(ctx context.Context, deviceIndex int, config *deviceCreationConfig) bool {
	agentName := filepath.Base(config.agentFolder(deviceIndex))
	enrollmentId := ""
	config.log.Infof("Approving device enrollment if exists for agent %s", agentName)

	// either it'll succeed or the agent routine will die and cancel this
	err := wait.PollUntilContextCancel(ctx, enrollmentCheckInterval, false, func(ctx context.Context) (bool, error) {
		if enrollmentId == "" {
			bannerFileData, err := readBannerFile(config.agentFolder(deviceIndex))
			if err != nil {
				config.log.Infof("Error reading banner file: %v", err)
				return false, nil
			}

			enrollmentId = testutil.GetEnrollmentIdFromText(bannerFileData)
			if enrollmentId == "" {
				config.log.Warnf("No enrollment id found in banner file %s", bannerFileData)
				return false, nil
			}

			config.log.Infof("Agent creation complete for %s", agentName)
		}

		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		resp, err := config.serviceClient.ApproveEnrollmentRequest(
			ctx,
			enrollmentId,
			v1alpha1.EnrollmentRequestApproval{
				Approved: true,
				Labels:   config.formattedLabels,
			})
		if err != nil {
			config.log.Errorf("Error approving device %s enrollment: %v", enrollmentId, err)
			return false, nil
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode >= 300 || resp.StatusCode < 200 {
			config.log.Errorf("Error approving device %s enrollment. http status: %d", enrollmentId, resp.StatusCode)
			return false, nil
		}
		config.log.Infof("Approved device enrollment %s", enrollmentId)

		return true, nil
	})

	if err != nil {
		if !errors.Is(err, context.Canceled) {
			config.log.Errorf("Error approving device enrollment: %v", err)
		}
		return false
	}
	return true
}

func createSingleDevice(ctx context.Context, deviceIndex int, config *deviceCreationConfig) (bool, <-chan struct{}) {
	// lifecycles are a little odd here.
	// if enrollment fails stop the agent process
	// if enrollment succeeds leave the agent process running
	// if the agent fails, stop the enrollment process
	approveCtx, approveCancel := context.WithCancel(ctx)
	devCtx, devCancel := context.WithCancel(ctx)

	// channel to allow the caller to know when the agent process has exited
	agentDone := make(chan struct{})

	// launch the agent process. If it exists for any reason, cancel the enrollment approval go routine
	go func() {
		defer close(agentDone)
		defer devCancel() // doesn't do much other than ensure resources are cleanup properly
		defer approveCancel()
		startAgentProcess(devCtx, deviceIndex, config)
	}()

	done := make(chan bool)
	go func() {
		defer close(done)
		done <- approveAgentEnrollment(approveCtx, deviceIndex, config)
	}()
	// if enrollment is successful, leave the agent running and return a channel to 'await'
	success := <-done
	if !success {
		devCancel()
		<-agentDone
	}
	return success, agentDone
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

func copyFile(log *logrus.Logger, from, to string) error {
	log.Tracef("Copying %s to %s", from, to)
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
