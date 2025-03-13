package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config/common"
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
	common.ServiceConfig
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

	readWriter fileio.ReadWriter
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
		ServiceConfig:        common.NewServiceConfig(),
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
	emptyEnrollmentService := common.EnrollmentService{}
	if cfg.EnrollmentService.Equal(&emptyEnrollmentService) {
		cfg.EnrollmentService = common.EnrollmentService{Config: *baseclient.NewDefault()}
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
	emptyManagementService := common.ManagementService{}
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
