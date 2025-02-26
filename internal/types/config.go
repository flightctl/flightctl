package types

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/yaml"
)

const (
	// TestRootDirEnvKey is the environment variable key used to set the file system root when testing.
	TestRootDirEnvKey = "FLIGHTCTL_TEST_ROOT_DIR"
)

// Config holds the information needed to connect to a Flight Control API server
type Config struct {
	Service  Service  `json:"service"`
	AuthInfo AuthInfo `json:"authentication"`

	// baseDir is used to resolve relative paths
	// If baseDir is empty, the current working directory is used.
	BaseDir string `json:"-"`
	// TestRootDir is the root directory for test files.
	TestRootDir string `json:"-"`
}

// Service contains information how to connect to and authenticate the Flight Control API server.
type Service struct {
	// Server is the URL of the Flight Control API server (the part before /api/v1/...).
	Server string `json:"server"`
	// TLSServerName is passed to the server for SNI and is used in the client to check server certificates against.
	// If TLSServerName is empty, the hostname used to contact the server is used.
	// +optional
	TLSServerName string `json:"tls-server-name,omitempty"`
	// CertificateAuthority is the path to a cert file for the certificate authority.
	CertificateAuthority string `json:"certificate-authority,omitempty"`
	// CertificateAuthorityData contains PEM-encoded certificate authority certificates. Overrides CertificateAuthority
	CertificateAuthorityData []byte `json:"certificate-authority-data,omitempty"`
	InsecureSkipVerify       bool   `json:"insecureSkipVerify,omitempty"`
}

// AuthInfo contains information for authenticating Flight Control API clients.
type AuthInfo struct {
	// ClientCertificate is the path to a client cert file for TLS.
	// +optional
	ClientCertificate string `json:"client-certificate,omitempty"`
	// ClientCertificateData contains PEM-encoded data from a client cert file for TLS. Overrides ClientCertificate.
	// +optional
	ClientCertificateData []byte `json:"client-certificate-data,omitempty"`
	// ClientKey is the path to a client key file for TLS.
	// +optional
	ClientKey string `json:"client-key,omitempty"`
	// ClientKeyData contains PEM-encoded data from a client key file for TLS. Overrides ClientKey.
	// +optional
	ClientKeyData []byte `json:"client-key-data,omitempty" datapolicy:"security-key"`
	// Bearer token for authentication
	// +optional
	Token string `json:"token,omitempty"`
}

func (c *Config) Equal(c2 *Config) bool {
	if c == c2 {
		return true
	}
	if c == nil || c2 == nil {
		return false
	}
	return c.Service.Equal(&c2.Service) && c.AuthInfo.Equal(&c2.AuthInfo)
}

func (s *Service) Equal(s2 *Service) bool {
	if s == s2 {
		return true
	}
	if s == nil || s2 == nil {
		return false
	}
	return s.Server == s2.Server && s.TLSServerName == s2.TLSServerName &&
		s.CertificateAuthority == s2.CertificateAuthority &&
		bytes.Equal(s.CertificateAuthorityData, s2.CertificateAuthorityData)
}

func (a *AuthInfo) Equal(a2 *AuthInfo) bool {
	if a == a2 {
		return true
	}
	if a == nil || a2 == nil {
		return false
	}
	return a.ClientCertificate == a2.ClientCertificate && a.ClientKey == a2.ClientKey &&
		bytes.Equal(a.ClientCertificateData, a2.ClientCertificateData) &&
		bytes.Equal(a.ClientKeyData, a2.ClientKeyData)
}

func (c *Config) DeepCopy() *Config {
	if c == nil {
		return nil
	}
	return &Config{
		Service:     *c.Service.DeepCopy(),
		AuthInfo:    *c.AuthInfo.DeepCopy(),
		BaseDir:     c.BaseDir,
		TestRootDir: c.TestRootDir,
	}
}

func (s *Service) DeepCopy() *Service {
	if s == nil {
		return nil
	}
	s2 := *s
	s2.CertificateAuthorityData = bytes.Clone(s.CertificateAuthorityData)
	return &s2
}

func (a *AuthInfo) DeepCopy() *AuthInfo {
	if a == nil {
		return nil
	}
	a2 := *a
	a2.ClientCertificateData = bytes.Clone(a.ClientCertificateData)
	a2.ClientKeyData = bytes.Clone(a.ClientKeyData)
	return &a2
}

func (c *Config) HasCredentials() bool {
	return (len(c.AuthInfo.ClientCertificate) > 0 || len(c.AuthInfo.ClientCertificateData) > 0) &&
		(len(c.AuthInfo.ClientKey) > 0 || len(c.AuthInfo.ClientKeyData) > 0)
}

func (c *Config) GetClientKeyPath() string {
	return resolvePath(c.AuthInfo.ClientKey, c.BaseDir)
}

func (c *Config) GetClientCertificatePath() string {
	return resolvePath(c.AuthInfo.ClientCertificate, c.BaseDir)
}

func (c *Config) SetBaseDir(baseDir string) {
	c.BaseDir = baseDir
}

func (c *Config) Persist(filename string) error {
	contents, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	directory := filename[:strings.LastIndex(filename, "/")]
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if err := os.WriteFile(filename, contents, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

func (c *Config) Validate() error {
	validationErrors := make([]error, 0)
	validationErrors = append(validationErrors, validateService(c.Service, c.BaseDir, c.TestRootDir)...)
	validationErrors = append(validationErrors, validateAuthInfo(c.AuthInfo, c.BaseDir, c.TestRootDir)...)
	if len(validationErrors) > 0 {
		return fmt.Errorf("invalid configuration: %v", utilerrors.NewAggregate(validationErrors).Error())
	}
	return nil
}

func validateService(service Service, baseDir string, testRootDir string) []error {
	validationErrors := make([]error, 0)
	// Make sure the server is specified and well-formed
	if len(service.Server) == 0 {
		validationErrors = append(validationErrors, fmt.Errorf("no server found"))
	} else {
		u, err := url.Parse(service.Server)
		if err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("invalid server format %q: %w", service.Server, err))
		} else {
			if len(u.Hostname()) == 0 {
				validationErrors = append(validationErrors, fmt.Errorf("invalid server format %q: no hostname", service.Server))
			}
		}
	}
	// Make sure CA data and CA file aren't both specified
	if len(service.CertificateAuthority) != 0 && len(service.CertificateAuthorityData) != 0 {
		validationErrors = append(validationErrors, fmt.Errorf("certificate-authority-data and certificate-authority are both specified. certificate-authority-data will override"))
	}
	if len(service.CertificateAuthority) != 0 {
		clientCertCA, err := os.Open(filepath.Join(testRootDir, resolvePath(service.CertificateAuthority, baseDir)))
		if err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("unable to read certificate-authority %v due to %w", service.CertificateAuthority, err))
		} else {
			defer clientCertCA.Close()
		}
	}
	return validationErrors
}

func validateAuthInfo(authInfo AuthInfo, baseDir string, testRootDir string) []error {
	validationErrors := make([]error, 0)
	if len(authInfo.ClientCertificate) != 0 || len(authInfo.ClientCertificateData) != 0 {
		// Make sure cert data and file aren't both specified
		if len(authInfo.ClientCertificate) != 0 && len(authInfo.ClientCertificateData) != 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client-cert-data and client-cert are both specified. client-cert-data will override"))
		}
		// Make sure key data and file aren't both specified
		if len(authInfo.ClientKey) != 0 && len(authInfo.ClientKeyData) != 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client-key-data and client-key are both specified; client-key-data will override"))
		}
		// Make sure a key is specified
		if len(authInfo.ClientKey) == 0 && len(authInfo.ClientKeyData) == 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client-key-data or client-key must be specified to use the clientCert authentication method"))
		}

		if len(authInfo.ClientCertificate) != 0 {
			clientCertFile, err := os.Open(filepath.Join(testRootDir, resolvePath(authInfo.ClientCertificate, baseDir)))
			if err != nil {
				validationErrors = append(validationErrors, fmt.Errorf("unable to read client-cert %v due to %w", authInfo.ClientCertificate, err))
			} else {
				defer clientCertFile.Close()
			}
		}
		if len(authInfo.ClientKey) != 0 {
			clientKeyFile, err := os.Open(filepath.Join(testRootDir, resolvePath(authInfo.ClientKey, baseDir)))
			if err != nil {
				validationErrors = append(validationErrors, fmt.Errorf("unable to read client-key %v due to %w", authInfo.ClientKey, err))
			} else {
				defer clientKeyFile.Close()
			}
		}
	}
	return validationErrors
}

// Reads the contents of all referenced files and embeds them in the config.
func (c *Config) Flatten() error {
	if err := flatten(&c.Service.CertificateAuthority, &c.Service.CertificateAuthorityData, c.BaseDir, c.TestRootDir); err != nil {
		return err
	}
	if err := flatten(&c.AuthInfo.ClientCertificate, &c.AuthInfo.ClientCertificateData, c.BaseDir, c.TestRootDir); err != nil {
		return err
	}
	if err := flatten(&c.AuthInfo.ClientKey, &c.AuthInfo.ClientKeyData, c.BaseDir, c.TestRootDir); err != nil {
		return err
	}
	return nil
}

func flatten(path *string, contents *[]byte, baseDir string, testRootDir string) error {
	if len(*path) != 0 {
		if len(*contents) > 0 {
			return errors.New("cannot have values for both path and contents")
		}

		var err error
		absPath := resolvePath(*path, baseDir)
		*contents, err = os.ReadFile(filepath.Join(testRootDir, absPath))
		if err != nil {
			return err
		}

		*path = ""
	}
	return nil
}

func resolvePath(path string, baseDir string) string {
	// Don't resolve empty paths
	if len(path) > 0 {
		// Don't resolve absolute paths
		if !filepath.IsAbs(path) {
			return filepath.Join(baseDir, path)
		}
	}
	return path
}
