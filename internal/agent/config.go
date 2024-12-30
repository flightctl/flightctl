package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
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
	// ConfigDir is the directory where the device's configuration is stored
	ConfigDir string `json:"-"`
	// DataDir is the directory where the device's data is stored
	DataDir string `json:"-"`

	// EnrollmentService is the client configuration for connecting to the device enrollment server
	EnrollmentService EnrollmentService `json:"enrollment-service,omitempty"`
	// ManagementService is the client configuration for connecting to the device management server
	ManagementService ManagementService `json:"management-service,omitempty"`

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

	reader fileio.Reader
}

type EnrollmentService struct {
	client.Config

	// EnrollmentUIEndpoint is the address of the device enrollment UI
	EnrollmentUIEndpoint string `json:"enrollment-ui-endpoint,omitempty"`
}

type ManagementService struct {
	client.Config
}

func (s *EnrollmentService) Equal(s2 *EnrollmentService) bool {
	if s == s2 {
		return true
	}
	return s.Config.Equal(&s2.Config) && s.EnrollmentUIEndpoint == s2.EnrollmentUIEndpoint
}

func (s *ManagementService) Equal(s2 *ManagementService) bool {
	if s == s2 {
		return true
	}
	return s.Config.Equal(&s2.Config)
}

func NewDefault() *Config {
	c := &Config{
		ConfigDir:            DefaultConfigDir,
		DataDir:              DefaultDataDir,
		EnrollmentService:    EnrollmentService{Config: *client.NewDefault()},
		ManagementService:    ManagementService{Config: *client.NewDefault()},
		StatusUpdateInterval: DefaultStatusUpdateInterval,
		SpecFetchInterval:    DefaultSpecFetchInterval,
		reader:               fileio.NewReader(),
		LogLevel:             logrus.InfoLevel.String(),
		DefaultLabels:        make(map[string]string),
	}

	if value := os.Getenv(TestRootDirEnvKey); value != "" {
		klog.Warning("Setting testRootDir is intended for testing only. Do not use in production.")
		c.testRootDir = filepath.Clean(value)
	}

	c.reader = fileio.NewReadWriter(fileio.WithTestRootDir(c.testRootDir))

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
	emptyEnrollmentService := EnrollmentService{}
	if cfg.EnrollmentService.Equal(&emptyEnrollmentService) {
		cfg.EnrollmentService = EnrollmentService{Config: *client.NewDefault()}
		cfg.EnrollmentService.Service = client.Service{
			Server:               DefaultManagementEndpoint,
			CertificateAuthority: filepath.Join(cfg.ConfigDir, DefaultCertsDirName, CacertFile),
		}
		cfg.EnrollmentService.AuthInfo = client.AuthInfo{
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
	emptyManagementService := ManagementService{}
	if cfg.ManagementService.Equal(&emptyManagementService) {
		cfg.ManagementService.Config = *cfg.EnrollmentService.Config.DeepCopy()
		cfg.ManagementService.Config.AuthInfo = client.AuthInfo{}
	}
	return nil
}

// Validate checks that the required fields are set and that the paths exist.
func (cfg *Config) Validate() error {
	if err := cfg.EnrollmentService.Validate(); err != nil {
		return err
	}
	if err := cfg.ManagementService.Validate(); err != nil {
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
			exists, err := cfg.reader.PathExists(field.value)
			if err != nil {
				return fmt.Errorf("%s: %w", field.name, err)
			}
			if !exists {
				return fmt.Errorf("%s does not exist: %s", field.name, field.value)
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
