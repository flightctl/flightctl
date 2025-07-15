package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
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
	// DefaultPullRetrySteps is the default retry attempts are allowed for pulling an OCI target.
	DefaultPullRetrySteps = 6
	// DefaultPullTimeout is the default timeout for pulling a single OCI
	// targets. Pull Timeout can not be greater that the prefetch timeout.
	DefaultPullTimeout = util.Duration(10 * time.Minute)
	// MinSyncInterval is the minimum interval allowed for the spec fetch and status update
	MinSyncInterval = util.Duration(2 * time.Second)
	// DefaultConfigDir is the default directory where the device's configuration is stored
	DefaultConfigDir = "/etc/flightctl"
	// DefaultConfigFile is the default path to the agent's configuration file
	DefaultConfigFile = DefaultConfigDir + "/config.yaml"
	// DefaultDataDir is the default directory where the device's data is stored
	DefaultDataDir = "/var/lib/flightctl"
	// SystemInfoCustomScriptDir is the directory where custom system info scripts are stored.
	SystemInfoCustomScriptDir = "/usr/lib/flightctl/custom-info.d"
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
	enrollmentMetricsCallback client.RPCMetricsCallback

	// managementMetricsCallback is a callback to report metrics about the management process.
	managementMetricsCallback client.RPCMetricsCallback

	// DefaultLabels are automatically applied to this device when the agent is enrolled in a service
	DefaultLabels map[string]string `json:"default-labels,omitempty"`

	// SystemInfo lists built-in system information keys to collect.
	SystemInfo []string `json:"system-info,omitempty"`

	// SystemInfoCustom defines keys used to collect custom system information.
	// Each key should match the name of an executable script in the custom info directory.
	// The script must output a single string, which will be included in device.status.systemInfo.CustomInfo.
	//
	// Keys must be camelCase with no spaces or special characters.
	// Script filenames may be camelCase or lowercase.
	SystemInfoCustom []string `json:"system-info-custom,omitempty"`

	// SystemInfoTimeout is the timeout for collecting system info.
	SystemInfoTimeout util.Duration `json:"system-info-timeout,omitempty"`

	// PullTimeout is the max duration a single OCI target will try to pull.
	PullTimeout util.Duration `json:"pull-timeout,omitempty"`

	// PullRetrySteps defines how many retry attempts are allowed for pulling an OCI target.
	PullRetrySteps int `json:"pull-retry-steps,omitempty"`

	// Certificates defines the certificates to be managed by the certificate manager.
	// These certificates are automatically provisioned, renewed, and stored based on their configuration.
	// Each certificate can use different provisioners (CSR, self-signed) and storage backends (filesystem).
	Certificates []provider.CertificateConfig `json:"certificates,omitempty"`

	readWriter fileio.ReadWriter
}

// DefaultSystemInfo defines the list of system information keys that are included
// in the default system info statud report generated by the agent.
var DefaultSystemInfo = []string{
	"hostname",
	"kernel",
	"distroName",
	"distroVersion",
	"productName",
	"productUuid",
	"productSerial",
	"netInterfaceDefault",
	"netIpDefault",
	"netMacDefault",
}

func NewDefault() *Config {
	c := &Config{
		ConfigDir:            DefaultConfigDir,
		DataDir:              DefaultDataDir,
		StatusUpdateInterval: DefaultStatusUpdateInterval,
		SpecFetchInterval:    DefaultSpecFetchInterval,
		readWriter:           fileio.NewReadWriter(),
		LogLevel:             logrus.InfoLevel.String(),
		DefaultLabels:        make(map[string]string),
		ServiceConfig:        config.NewServiceConfig(),
		SystemInfo:           DefaultSystemInfo,
		SystemInfoTimeout:    DefaultSystemInfoTimeout,
		PullTimeout:          DefaultPullTimeout,
		PullRetrySteps:       DefaultPullRetrySteps,
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

func (cfg *Config) SetEnrollmentMetricsCallback(cb client.RPCMetricsCallback) {
	cfg.enrollmentMetricsCallback = cb
}

func (cfg *Config) GetEnrollmentMetricsCallback() client.RPCMetricsCallback {
	return cfg.enrollmentMetricsCallback
}

func (cfg *Config) SetManagementMetricsCallback(cb client.RPCMetricsCallback) {
	cfg.managementMetricsCallback = cb
}

func (cfg *Config) GetManagementMetricsCallback() client.RPCMetricsCallback {
	return cfg.managementMetricsCallback
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

	requiredFields := []struct {
		value     string
		name      string
		checkPath bool
	}{
		{cfg.ConfigDir, "config-dir", true},
		{cfg.DataDir, "data-dir", true},
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

func (cfg *Config) validateSyncIntervals() error {
	if cfg.SpecFetchInterval < MinSyncInterval {
		return fmt.Errorf("minimum spec fetch interval is %s have %s", MinSyncInterval, cfg.SpecFetchInterval)
	}
	if cfg.StatusUpdateInterval < MinSyncInterval {
		return fmt.Errorf("minimum status update interval is %s have %s", MinSyncInterval, cfg.StatusUpdateInterval)
	}
	return nil
}

func (cfg *Config) LoadWithOverrides(configFile string) error {
	if err := cfg.ParseConfigFile(configFile); err != nil {
		return err
	}

	confSubdir := filepath.Join(filepath.Dir(configFile), "conf.d")
	entries, err := cfg.readWriter.ReadDir(confSubdir)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg.Complete()
		}
		return err
	}

	re := regexp.MustCompile(`^.*\.ya?ml$`)
	for _, entry := range entries {
		if entry.IsDir() || !re.MatchString(entry.Name()) {
			continue
		}
		overrideCfg := &Config{}
		overridePath := filepath.Join(confSubdir, entry.Name())
		contents, err := cfg.readWriter.ReadFile(overridePath)
		if err != nil {
			return fmt.Errorf("reading override config %s: %w", overridePath, err)
		}
		if err := yaml.Unmarshal(contents, overrideCfg); err != nil {
			return fmt.Errorf("unmarshalling override config %s: %w", overridePath, err)
		}
		mergeConfigs(cfg, overrideCfg)
	}

	if err := cfg.Complete(); err != nil {
		return err
	}
	return cfg.Validate()
}

func mergeConfigs(base, override *Config) {
	// log
	overrideIfNotEmpty(&base.LogLevel, override.LogLevel)
	overrideIfNotEmpty(&base.LogPrefix, override.LogPrefix)

	// system info
	overrideSliceIfNotNil(&base.SystemInfo, override.SystemInfo)
	overrideSliceIfNotNil(&base.SystemInfoCustom, override.SystemInfoCustom)
	overrideIfNotEmpty(&base.SystemInfoTimeout, override.SystemInfoTimeout)

	// certificates
	overrideSliceIfNotNil(&base.Certificates, override.Certificates)

	for k, v := range override.DefaultLabels {
		base.DefaultLabels[k] = v
	}
}

func Load(configFile string) (*Config, error) {
	cfg := NewDefault()
	if err := cfg.LoadWithOverrides(configFile); err != nil {
		return nil, err
	}
	return cfg, nil
}

// overrideIfNotEmpty replaces dst with src only if src is not empty.
func overrideIfNotEmpty[T any](dst *T, src T) {
	var empty T
	if !reflect.DeepEqual(empty, src) {
		*dst = src
	}
}

// overrideSliceIfNotNil replaces dst with src only if src is not nil.
// This allows default values to remain when a field is omitted,
// while supporting explicit empty lists (`system-info: []`) to disable features.
func overrideSliceIfNotNil[T any](dst *[]T, src []T) {
	if src != nil {
		*dst = src
	}
}
