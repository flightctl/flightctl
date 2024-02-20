package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	"gopkg.in/yaml.v2"
	"k8s.io/klog/v2"
)

const (
	// DefaultFetchSpecInterval is the default interval between two reads of the remote device spec
	DefaultFetchSpecInterval = time.Second * 60

	// DefaultStatusUpdateInterval is the default interval between two status updates
	DefaultStatusUpdateInterval = time.Second * 60
)

type Config struct {
	// ManagementEndpoint is the URL of the device management server
	ManagementEndpoint string `json:"managementEndpoint,omitempty"`
	// EnrollmentEndpoint is the URL of the device enrollment server
	EnrollmentEndpoint string `json:"enrollmentEndpoint,omitempty"`
	// CertDir is the directory where the device's certificates are stored
	CertDir string `json:"certDir,omitempty"`
	// TPMPath is the path to the TPM device
	TPMPath string `json:"tpmPath,omitempty"`
	// FetchSpecInterval is the interval between two reads of the remote device spec
	FetchSpecInterval util.Duration `json:"fetchSpecInterval,omitempty"`
	// StatusUpdateInterval is the interval between two status updates
	StatusUpdateInterval util.Duration `json:"statusUpdateInterval,omitempty"`
	// LogPrefix is the log prefix used for testing
	LogPrefix string `json:"logPrefix,omitempty"`

	// testRootDir is the root directory of the test agent
	testRootDir string
	// enrollmentMetricsCallback is a callback to report metrics about the enrollment process.
	enrollmentMetricsCallback func(operation string, duractionSeconds float64, err error)
}

func NewDefault() *Config {
	return &Config{
		ManagementEndpoint:   "https://localhost:3333",
		StatusUpdateInterval: util.Duration(DefaultStatusUpdateInterval),
		FetchSpecInterval:    util.Duration(DefaultFetchSpecInterval),
	}
}

func (cfg *Config) SetTestRootDir(rootDir string) {
	klog.Warning("Setting testRootDir is intended for testing only. Do not use in production.")
	cfg.testRootDir = rootDir
}

func (cfg *Config) GetTestRootDir() string {
	return cfg.testRootDir
}

func (cfg *Config) SetEnrollmentMetricsCallback(cb func(operation string, duractionSeconds float64, err error)) {
	cfg.enrollmentMetricsCallback = cb
}

// TODO: dedupe with internal/config/config.go
func NewFromFile(cfgFile string) (*Config, error) {
	cfg, err := Load(cfgFile)
	if err != nil {
		return nil, err
	}
	if err := Validate(cfg); err != nil {
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

func Validate(cfg *Config) error {
	return nil
}

func (cfg *Config) String() string {
	contents, err := json.Marshal(cfg)
	if err != nil {
		return "<error>"
	}
	return string(contents)
}
