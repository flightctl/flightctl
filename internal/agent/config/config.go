package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/sirupsen/logrus"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

const (
	// DefaultSpecFetchInterval is the default interval between two reads of the remote device spec
	DefaultSpecFetchInterval = util.Duration(60 * time.Second)
	// DefaultStatusUpdateInterval is the default interval between two status updates
	DefaultStatusUpdateInterval = util.Duration(60 * time.Second)
	// DefaultSystemInfoTimeout is the default timeout for collecting system info
	DefaultSystemInfoTimeout = util.Duration(2 * time.Minute)
	// MinSyncInterval is the minimum interval allowed for the spec fetch and status update
	MinSyncInterval = util.Duration(2 * time.Second)
	// DefaultConfigDir is the default directory where the device's configuration is stored
	DefaultConfigDir = "/etc/flightctl"
	// DefaultConfigFile is the default path to the agent's configuration file
	DefaultConfigFile = DefaultConfigDir + "/config.yaml"
	// DefaultDataDir is the default directory where the device's data is stored
	DefaultDataDir = "/var/lib/flightctl"
	// DefaultCertsDir is the default directory where the device's certificates are stored
	DefaultCertsDirName = "certs"
	// DefaultManagementEndpoint is the default address of the device management server
	DefaultManagementEndpoint = "https://localhost:7443"
	// name of the CA bundle file
	CacertFile = "ca.crt"
	// GeneratedCertFile is the name of the cert file which is generated as the result of enrollment
	GeneratedCertFile = "agent.crt"
	// name of the agent's key file
	KeyFile = "agent.key"
	// name of the enrollment certificate file
	EnrollmentCertFile = "client-enrollment.crt"
	// name of the enrollment key file
	EnrollmentKeyFile = "client-enrollment.key"
	// TestRootDirEnvKey is the environment variable key used to set the file system root when testing.
	TestRootDirEnvKey = "FLIGHTCTL_TEST_ROOT_DIR"
)

type Config struct {
	config.ServiceConfig

	// ConfigDir is the directory where the device's configuration is stored
	ConfigDir string `json:"-"`
	// DataDir is the directory where the device's data is stored
	DataDir string `json:"-"`

	// SpecFetchInterval is the interval between two reads of the remote device spec
	SpecFetchInterval util.Duration `json:"spec-fetch-interval,omitempty"`
	// StatusUpdateInterval is the interval between two status updates
	StatusUpdateInterval util.Duration `json:"status-update-interval,omitempty"`

	// TPMPath is the path to the TPM device
	TPMPath string `json:"tpm-path,omitempty"`

	// LogLevel is the level of logging. can be:  "panic", "fatal", "error", "warn"/"warning",
	// "info", "debug" or "trace", any other will be treated as "info"
	LogLevel string `json:"log-level,omitempty"`
	// LogPrefix is the log prefix used for testing
	LogPrefix string `json:"log-prefix,omitempty"`

	// testRootDir is the root directory of the test agent
	testRootDir string
	// enrollmentMetricsCallback is a callback to report metrics about the enrollment process.
	enrollmentMetricsCallback func(operation string, durationSeconds float64, err error)

	// DefaultLabels are automatically applied to this device when the agent is enrolled in a service
	DefaultLabels map[string]string `json:"default-labels,omitempty"`

	// SystemInfoKeys optionally replaces the default set of system info keys
	// collected and exposed by the agent. If unset, system.DefaultInfoKeys is used.
	SystemInfoKeys []string `json:"system-info-keys,omitempty"`

	// CustomSystemInfoKeys are user-defined keys that can be used to collect
	// additional information. The expectation is that an executable with the
	// same name as the key exists in the system path. The output of the
	// executable will be collected and added to the systemInfo status. If the
	// script returns a non-zero exit code or does not exist, the key will have
	// an empty value.
	CustomSystemInfoKeys []string `json:"custom-system-info,omitempty"`

	// CollectSystemInfoTimeout is the timeout for collecting system info.
	CollectSystemInfoTimeout util.Duration `json:"collect-info-timeout,omitempty"`

	readWriter fileio.ReadWriter
}

func NewDefault() *Config {
	c := &Config{
		ConfigDir:                DefaultConfigDir,
		DataDir:                  DefaultDataDir,
		StatusUpdateInterval:     DefaultStatusUpdateInterval,
		SpecFetchInterval:        DefaultSpecFetchInterval,
		readWriter:               fileio.NewReadWriter(),
		LogLevel:                 logrus.InfoLevel.String(),
		DefaultLabels:            make(map[string]string),
		ServiceConfig:            config.NewServiceConfig(),
		SystemInfoKeys:           systeminfo.DefaultInfoKeys,
		CollectSystemInfoTimeout: DefaultSystemInfoTimeout,
	}

	if value := os.Getenv(TestRootDirEnvKey); value != "" {
		klog.Warning("Setting testRootDir is intended for testing only. Do not use in production.")
		c.testRootDir = filepath.Clean(value)
	}

	c.readWriter = fileio.NewReadWriter(fileio.WithTestRootDir(c.testRootDir))

	return c
}

func (cfg *Config) GetTestRootDir() string {
	return cfg.testRootDir
}

// Some files are handled from the crypto modules that don't work with our device fileio
// and need to know the real paths
// TODO: potentially unify all file writer/readers under some mockable interface
func (cfg *Config) PathFor(filePath string) string {
	return path.Join(cfg.testRootDir, filePath)
}

func (cfg *Config) SetEnrollmentMetricsCallback(cb func(operation string, duractionSeconds float64, err error)) {
	cfg.enrollmentMetricsCallback = cb
}

// Complete fills in defaults for fields not set by the config file
func (cfg *Config) Complete() error {
	// If the enrollment service hasn't been specified, attempt using the default local dev env.
	emptyEnrollmentService := config.EnrollmentService{}
	if cfg.EnrollmentService.Equal(&emptyEnrollmentService) {
		cfg.EnrollmentService = config.EnrollmentService{Config: *baseclient.NewDefault()}
		cfg.EnrollmentService.Service = baseclient.Service{
			Server:               DefaultManagementEndpoint,
			CertificateAuthority: filepath.Join(cfg.ConfigDir, DefaultCertsDirName, CacertFile),
		}
		cfg.EnrollmentService.AuthInfo = baseclient.AuthInfo{
			ClientCertificate: filepath.Join(cfg.DataDir, DefaultCertsDirName, EnrollmentCertFile),
			ClientKey:         filepath.Join(cfg.DataDir, DefaultCertsDirName, EnrollmentKeyFile),
		}
		cfg.EnrollmentService.EnrollmentUIEndpoint = DefaultManagementEndpoint
	}
	// If the enrollment UI endpoint hasn't been specified, attempt using the same endpoint as the enrollment service.
	if cfg.EnrollmentService.EnrollmentUIEndpoint == "" {
		cfg.EnrollmentService.EnrollmentUIEndpoint = cfg.EnrollmentService.Config.Service.Server
	}
	// If the management service hasn't been specified, attempt using the same endpoint as the enrollment service,
	// but clear the auth info.
	emptyManagementService := config.ManagementService{}
	if cfg.ManagementService.Equal(&emptyManagementService) {
		cfg.ManagementService.Config = *cfg.EnrollmentService.Config.DeepCopy()
		cfg.ManagementService.Config.AuthInfo = baseclient.AuthInfo{}
	}
	return nil
}

// Validate checks that the required fields are set and ensures that the paths exist.
func (cfg *Config) Validate() error {
	if err := cfg.EnrollmentService.Validate(); err != nil {
		return err
	}
	if err := cfg.ManagementService.Validate(); err != nil {
		return err
	}

	if err := cfg.validateSyncIntervals(); err != nil {
		return err
	}
	if err := cfg.validateSystemInfoKeys(); err != nil {
		return err
	}

	requiredFields := []struct {
		value     string
		name      string
		checkPath bool
	}{
		{cfg.ConfigDir, "config-dir", true},
		{cfg.DataDir, "data-dir", true},
		{filepath.Join(cfg.DataDir, systeminfo.ScriptOverrideDir), "system-info-override-dir", true},
	}

	for _, field := range requiredFields {
		if field.value == "" {
			return fmt.Errorf("%s is required", field.name)
		}
		if field.checkPath {
			exists, err := cfg.readWriter.PathExists(field.value)
			if err != nil {
				return fmt.Errorf("%s: %w", field.name, err)
			}
			if !exists {
				// ensure required paths exist
				klog.Infof("Creating missing required directory: %s", field.value)
				if err := cfg.readWriter.MkdirAll(field.value, fileio.DefaultDirectoryPermissions); err != nil {
					return fmt.Errorf("creating %s: %w", field.name, err)
				}
			}
		}
	}

	return nil
}

// ParseConfigFile reads the config file and unmarshals it into the Config struct
func (cfg *Config) ParseConfigFile(cfgFile string) error {
	contents, err := cfg.readWriter.ReadFile(cfgFile)
	if err != nil {
		return fmt.Errorf("reading config file: %w", err)
	}
	if err := yaml.Unmarshal(contents, cfg); err != nil {
		return fmt.Errorf("unmarshalling config file: %w", err)
	}
	cfg.EnrollmentService.Config.SetBaseDir(filepath.Dir(cfgFile))
	cfg.ManagementService.Config.SetBaseDir(filepath.Dir(cfgFile))
	return nil
}

func (cfg *Config) String() string {
	contents, err := json.Marshal(cfg)
	if err != nil {
		return "<error>"
	}
	return string(contents)
}

func (cfg *Config) MergedInfoKeys() []string {
	keySet := make(map[string]struct{})

	keys := cfg.SystemInfoKeys
	if len(keys) == 0 {
		keys = systeminfo.DefaultInfoKeys
	}
	for _, k := range keys {
		keySet[k] = struct{}{}
	}

	// custom script-based keys
	for _, k := range cfg.CustomSystemInfoKeys {
		keySet[k] = struct{}{}
	}

	merged := make([]string, 0, len(keySet))
	for k := range keySet {
		merged = append(merged, k)
	}
	sort.Strings(merged)
	return merged
}

func (cfg *Config) validateSyncIntervals() error {
	if cfg.SpecFetchInterval < MinSyncInterval {
		return fmt.Errorf("minimum spec fetch interval is %s have %s", MinSyncInterval, cfg.SpecFetchInterval)
	}
	if cfg.StatusUpdateInterval < MinSyncInterval {
		return fmt.Errorf("minimum status update interval is %s have %s", MinSyncInterval, cfg.StatusUpdateInterval)
	}
	return nil
}

func (cfg *Config) validateSystemInfoKeys() error {
	for _, key := range cfg.SystemInfoKeys {
		if _, ok := systeminfo.SupportedInfoKeys[key]; !ok {
			return fmt.Errorf("unsupported system-info-key: %q", key)
		}
	}

	// custom info key validation is best effort
	for _, key := range cfg.CustomSystemInfoKeys {
		scriptPath := filepath.Join(cfg.DataDir, systeminfo.ScriptOverrideDir, key)
		exists, err := cfg.readWriter.PathExists(scriptPath)
		if err != nil {
			klog.Errorf("Checking custom info key %q: %v", key, err)
		}
		if !exists {
			klog.Errorf("Custom info key collector %q does not exist: %s", key, scriptPath)
		}
	}
	return nil
}
