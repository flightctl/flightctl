package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
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
	// DefaultConfigDir is the default directory where the device's configuration is stored
	DefaultConfigDir = "/etc/flightctl"
	// DefaultConfigFile is the default path to the agent's configuration file
	DefaultConfigFile = DefaultConfigDir + "/config.yaml"
	// DefaultDataDir is the default directory where the device's data is stored
	DefaultDataDir = "/var/lib/flightctl"
	// DefaultCertsDir is the default directory where the device's certificates are stored
	DefaultCertsDirName = "certs"
	// DefaultManagementEndpoint is the default address of the device management server
	DefaultManagementEndpoint = "https://localhost:3333"
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
	// Key is the path to the agent's private key
	Key string `json:"key"`
	// Cacert is the path to the CA certificate
	Cacert string `json:"ca-cert"`
	// GenerateCert is the path to the cert file which is generated as the result of enrollment
	GeneratedCert string `json:"generated-cert"`
	// DataDir is the directory where the device's data is stored
	DataDir string `json:"data-dir"`
	// ConfigDir is the directory where the device's configuration is stored
	ConfigDir string `json:"config-dir"`
	// EnrollmentCertFile is the path to the enrollment certificate
	EnrollmentCertFile string `json:"enrollment-cert-file"`
	// EnrollmentKeyFile is the path to the enrollment key
	EnrollmentKeyFile string `json:"enrollment-key-file"`
	// ManagementEndpoint is the address of the device management server
	ManagementEndpoint string `json:"management-endpoint,omitempty"`
	// EnrollmentEndpoint is the address of the device enrollment server
	EnrollmentEndpoint string `json:"enrollment-endpoint,omitempty"`
	// EnrollmentUIEndpoint is the address of the device enrollment UI
	EnrollmentUIEndpoint string `json:"enrollment-ui-endpoint,omitempty"`
	// TPMPath is the path to the TPM device
	TPMPath string `json:"tpm-path,omitempty"`
	// SpecFetchInterval is the interval between two reads of the remote device spec
	SpecFetchInterval util.Duration `json:"spec-fetch-interval,omitempty"`
	// StatusUpdateInterval is the interval between two status updates
	StatusUpdateInterval util.Duration `json:"status-update-interval,omitempty"`
	// LogPrefix is the log prefix used for testing
	LogPrefix string `json:"log-prefix,omitempty"`

	// testRootDir is the root directory of the test agent
	testRootDir string
	// enrollmentMetricsCallback is a callback to report metrics about the enrollment process.
	enrollmentMetricsCallback func(operation string, durationSeconds float64, err error)

	// LogLevel is the level of logging. can be:  "panic", "fatal", "error", "warn"/"warning",
	// "info", "debug" or "trace", any other will be treated as "info"
	LogLevel string `yaml:"log-level,omitempty"`

	reader *fileio.Reader
}

func NewDefault() *Config {
	c := &Config{
		ManagementEndpoint:   DefaultManagementEndpoint,
		EnrollmentEndpoint:   DefaultManagementEndpoint,
		EnrollmentUIEndpoint: DefaultManagementEndpoint,
		ConfigDir:            DefaultConfigDir,
		DataDir:              DefaultDataDir,
		Cacert:               filepath.Join(DefaultConfigDir, DefaultCertsDirName, CacertFile),
		Key:                  filepath.Join(DefaultDataDir, DefaultCertsDirName, KeyFile),
		GeneratedCert:        filepath.Join(DefaultDataDir, DefaultCertsDirName, GeneratedCertFile),
		EnrollmentCertFile:   filepath.Join(DefaultConfigDir, DefaultCertsDirName, EnrollmentCertFile),
		EnrollmentKeyFile:    filepath.Join(DefaultConfigDir, DefaultCertsDirName, EnrollmentKeyFile),
		StatusUpdateInterval: DefaultStatusUpdateInterval,
		SpecFetchInterval:    DefaultSpecFetchInterval,
		reader:               fileio.NewReader(),
		LogLevel:             logrus.InfoLevel.String(),
	}

	if value := os.Getenv(TestRootDirEnvKey); value != "" {
		klog.Warning("Setting testRootDir is intended for testing only. Do not use in production.")
		c.testRootDir = filepath.Clean(value)
		c.reader.SetRootdir(c.testRootDir)
	}

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

// Validate checks that the required fields are set and that the paths exist.
func (cfg *Config) Validate() error {
	requiredFields := []struct {
		value     string
		name      string
		checkPath bool
	}{
		{cfg.ManagementEndpoint, "management-endpoint", false},
		{cfg.EnrollmentEndpoint, "enrollment-endpoint", false},
		{cfg.GeneratedCert, "generated-cert", false},
		{cfg.ConfigDir, "config-dir", true},
		{cfg.DataDir, "data-dir", true},
		{cfg.Cacert, "ca-cert", true},
		{cfg.Key, "key", false},
		{cfg.EnrollmentCertFile, "enrollment-cert-file", true},
		{cfg.EnrollmentKeyFile, "enrollment-key-file", true},
	}

	for _, field := range requiredFields {
		if field.value == "" {
			return fmt.Errorf("%s is required", field.name)
		}
		if field.checkPath {
			if err := cfg.reader.CheckPathExists(field.value); err != nil {
				return fmt.Errorf("%s: %w", field.name, err)
			}
		}
	}

	return nil
}

// ParseConfigFile reads the config file and unmarshals it into the Config struct
func (cfg *Config) ParseConfigFile(cfgFile string) error {
	contents, err := cfg.reader.ReadFile(cfgFile)
	if err != nil {
		return fmt.Errorf("reading config file: %w", err)
	}
	if err := yaml.Unmarshal(contents, cfg); err != nil {
		return fmt.Errorf("unmarshalling config file: %w", err)
	}
	return nil
}

func (cfg *Config) String() string {
	contents, err := json.Marshal(cfg)
	if err != nil {
		return "<error>"
	}
	return string(contents)
}
