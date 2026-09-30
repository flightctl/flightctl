package flightctldestination

import (
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

const defaultTimeout = 30 * time.Second

// AuthConfig holds the authentication configuration for the Flightctl
// destination.
type AuthConfig struct {
	Authenticator string `json:"authenticator"`
}

// Config holds the provider-specific configuration for the Flightctl
// destination. It implements [config.Validator].
type Config struct {
	Server               string         `json:"server"`
	CertificateAuthority string         `json:"certificateAuthority,omitempty"`
	InsecureSkipVerify   bool           `json:"insecureSkipVerify,omitempty"`
	OrgID                string         `json:"orgId,omitempty"`
	Auth                 *AuthConfig    `json:"auth,omitempty"`
	Timeout              *util.Duration `json:"timeout,omitempty"`
}

func (c *Config) Validate() error {
	if c.Server == "" {
		return fmt.Errorf("missing required field \"server\"")
	}
	if c.Auth != nil && c.Auth.Authenticator == "" {
		return fmt.Errorf("auth.authenticator must not be empty when auth is configured")
	}
	if c.Timeout != nil && time.Duration(*c.Timeout) <= 0 {
		return fmt.Errorf("timeout must be positive when set, got %s", time.Duration(*c.Timeout))
	}
	if c.OrgID != "" {
		if _, err := uuid.Parse(c.OrgID); err != nil {
			return fmt.Errorf("orgId %q is not a valid UUID: %w", c.OrgID, err)
		}
	}
	return nil
}

func (c *Config) timeoutDuration() time.Duration {
	if c.Timeout != nil {
		return time.Duration(*c.Timeout)
	}
	return defaultTimeout
}
