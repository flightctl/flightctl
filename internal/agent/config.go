package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

const (
	// DefaultSpecFetchInterval is the default interval between two reads of the remote device spec
	DefaultSpecFetchInterval = time.Second * 60
	// DefaultStatusUpdateInterval is the default interval between two status updates
	DefaultStatusUpdateInterval = time.Second * 60
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
)

type Config struct {
	// Key is the path to the agent's private key
	Key string `yaml:"key"`
	// Cacert is the path to the CA certificate
	Cacert string `yaml:"ca-cert"`
	// GenerateCert is the path to the cert file which is generated as the result of enrollment
	GeneratedCert string `yaml:"generated-cert"`
	// DataDir is the directory where the device's data is stored
	DataDir string `yaml:"data-dir"`
	// ConfigDir is the directory where the device's configuration is stored
	ConfigDir string `yaml:"config-dir"`
	// EnrollmentCertFile is the path to the enrollment certificate
	EnrollmentCertFile string `yaml:"enrollment-cert-file"`
	// EnrollmentKeyFile is the path to the enrollment key
	EnrollmentKeyFile string `yaml:"enrollment-key-file"`
	// ManagementEndpoint is the address of the device management server
	ManagementEndpoint string `yaml:"management-endpoint,omitempty"`
	// EnrollmentEndpoint is the address of the device enrollment server
	EnrollmentEndpoint string `yaml:"enrollment-endpoint,omitempty"`
	// EnrollmentUIEndpoint is the address of the device enrollment UI
	EnrollmentUIEndpoint string `yaml:"enrollment-ui-endpoint,omitempty"`
	// TPMPath is the path to the TPM device
	TPMPath string `yaml:"tpm-path,omitempty"`
	// SpecFetchInterval is the interval between two reads of the remote device spec
	SpecFetchInterval time.Duration `yaml:"spec-fetch-interval,omitempty"`
	// StatusUpdateInterval is the interval between two status updates
	StatusUpdateInterval time.Duration `yaml:"status-update-interval,omitempty"`
	// LogPrefix is the log prefix used for testing
	LogPrefix string `yaml:"log-prefix,omitempty"`

	// testRootDir is the root directory of the test agent
	testRootDir string
	// enrollmentMetricsCallback is a callback to report metrics about the enrollment process.
	enrollmentMetricsCallback func(operation string, durationSeconds float64, err error)

	reader *fileio.Reader
}

func NewDefault() *Config {
	return &Config{
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
	}
}

func (cfg *Config) SetTestRootDir(rootDir string) {
	klog.Warning("Setting testRootDir is intended for testing only. Do not use in production.")
	if cfg.reader != nil {
		cfg.reader.SetRootdir(rootDir)
	}
	cfg.testRootDir = rootDir
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

func NewFromFile(cfgFile string) (*Config, error) {
	cfg, err := Load(cfgFile)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func LoadOrGenerate(cfgFile string) (*Config, error) {
	if _, err := os.Stat(cfgFile); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(cfgFile), os.FileMode(0755)); err != nil {
			return nil, fmt.Errorf("creating directory for config file: %v", err)
		}
		if err := Save(NewDefault(), cfgFile); err != nil {
			return nil, err
		}
	}
	return NewFromFile(cfgFile)
}

func Load(cfgFile string) (*Config, error) {
	contents, err := os.ReadFile(cfgFile)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %v", err)
	}
	c := &Config{}
	if err := yaml.Unmarshal(contents, c); err != nil {
		return nil, fmt.Errorf("decoding config: %v", err)
	}
	return c, nil
}

func Save(cfg *Config, cfgFile string) error {
	contents, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config: %v", err)
	}
	if err := os.WriteFile(cfgFile, contents, 0600); err != nil {
		return fmt.Errorf("writing config file: %v", err)
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
