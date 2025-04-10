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

func defaultConfigFilePath() string {
	return filepath.Join(util.MustString(os.UserHomeDir), "."+appName, "agent.yaml")
}

func defaultDataDir() string {
	return filepath.Join(util.MustString(os.UserHomeDir), "."+appName, "data")
}

type cliConfig struct {
	ConfigFile         string
	DataDir            string
	Labels             []string
	NumDevices         int
	InitialDeviceIndex int
	MetricsAddr        string
	StopAfter          time.Duration
	VersionInfo        bool
	VersionFormat      string
	LogLevel           string
}

func parseKeyValueArgs() (*cliConfig, error) {
	cfg := &cliConfig{
		ConfigFile:         defaultConfigFilePath(),
		DataDir:            defaultDataDir(),
		Labels:             []string{},
		NumDevices:         1,
		InitialDeviceIndex: 0,
		MetricsAddr:        "localhost:9093",
		LogLevel:           "debug",
	}

	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "-") {
			continue // skip any unknown flags
		}
		keyVal := strings.SplitN(arg, "=", 2)
		if len(keyVal) != 2 {
			return nil, fmt.Errorf("invalid argument format: %s, expected key=value", arg)
		}
		key := keyVal[0]
		val := keyVal[1]
		switch key {
		case "config":
			cfg.ConfigFile = val
		case "data-dir":
			cfg.DataDir = val
		case "label":
			cfg.Labels = append(cfg.Labels, val)
		case "count":
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid count: %v", err)
			}
			cfg.NumDevices = n
		case "initial-device-index":
			idx, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid initial-device-index: %v", err)
			}
			cfg.InitialDeviceIndex = idx
		case "metrics":
			cfg.MetricsAddr = val
		case "stop-after":
			dur, err := time.ParseDuration(val)
			if err != nil {
				return nil, fmt.Errorf("invalid stop-after duration: %v", err)
			}
			cfg.StopAfter = dur
		case "version":
			cfg.VersionInfo = val == "true"
		case "output":
			cfg.VersionFormat = val
		case "log-level":
			cfg.LogLevel = val
		default:
			return nil, fmt.Errorf("unknown argument: %s", key)
		}
	}
	return cfg, nil
}

func main() {
	log := flightlog.InitLogs()

	cfg, err := parseKeyValueArgs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing arguments: %v\n", err)
		os.Exit(1)
	}

	logLvl, err := logrus.ParseLevel(cfg.LogLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid log level: %s\n", cfg.LogLevel)
		os.Exit(1)
	}
	log.SetLevel(logLvl)

	if cfg.VersionInfo {
		if err := reportVersion(&cfg.VersionFormat); err != nil {
			fmt.Println(err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}

	log.Infof("starting device simulator with %d devices", cfg.NumDevices)
	formattedLabels := formatLabels(&cfg.Labels)

	agentConfigTemplate := createAgentConfigTemplate(cfg.DataDir, cfg.ConfigFile)

	log.Infoln("setting up metrics endpoint")
	setupMetricsEndpoint(cfg.MetricsAddr)

	serviceClient, err := client.NewFromConfigFile(client.DefaultFlightctlClientConfigPath())
	if err != nil {
		log.Fatalf("Error creating service client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agents, agentsFolders := createAgents(log, cfg.NumDevices, cfg.InitialDeviceIndex, agentConfigTemplate)

	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigShutdown
		log.Printf("Shutdown signal received (%v).", sig)
		cancel()
	}()

	for i := 0; i < cfg.NumDevices; i++ {
		time.Sleep(time.Duration(rand.Float64() * float64(agentConfigTemplate.StatusUpdateInterval))) //nolint:gosec
		agent := agents[i]
		go startAgent(ctx, agent, log, i)
		go approveAgent(ctx, log, serviceClient, agentsFolders[i], formattedLabels)
	}

	if cfg.StopAfter > 0 {
		time.AfterFunc(cfg.StopAfter, func() {
			log.Infoln("stopping simulator after duration")
			cancel()
		})
	}

	<-ctx.Done()
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

func formatLabels(lableArgs *[]string) *map[string]string {
	formattedLabels := map[string]string{}

	if lableArgs != nil {
		formattedLabels = util.LabelArrayToMap(*lableArgs)
	}

	formattedLabels["created_by"] = "device-simulator"
	return &formattedLabels
}
