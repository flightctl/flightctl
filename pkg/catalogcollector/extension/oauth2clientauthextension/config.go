package oauth2clientauthextension

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/util"
)

const (
	defaultTimeout      = 30 * time.Second
	defaultExpiryBuffer = 10 * time.Second
)

// Config holds the configuration for the OAuth2 client-credentials
// authentication extension.
type Config struct {
	ClientID             string            `json:"clientId,omitempty"`
	ClientIDFile         string            `json:"clientIdFile,omitempty"`
	ClientSecret         api.SecureString  `json:"clientSecret,omitempty"`
	ClientSecretFile     string            `json:"clientSecretFile,omitempty"`
	TokenURL             string            `json:"tokenUrl"`
	Scopes               []string          `json:"scopes,omitempty"`
	EndpointParams       map[string]string `json:"endpointParams,omitempty"`
	CertificateAuthority string            `json:"certificateAuthority,omitempty"`
	InsecureSkipVerify   bool              `json:"insecureSkipVerify,omitempty"`
	Timeout              *util.Duration    `json:"timeout,omitempty"`
	ExpiryBuffer         *util.Duration    `json:"expiryBuffer,omitempty"`
}

func (c *Config) Validate() error {
	hasID := strings.TrimSpace(c.ClientID) != ""
	hasIDFile := strings.TrimSpace(c.ClientIDFile) != ""

	if hasID && hasIDFile {
		return fmt.Errorf(
			"\"clientId\" and \"clientIdFile\" are mutually exclusive",
		)
	}
	if !hasID && !hasIDFile {
		return fmt.Errorf(
			"one of \"clientId\" or \"clientIdFile\" must be set",
		)
	}

	hasSecret := c.ClientSecret.Value() != ""
	hasSecretFile := strings.TrimSpace(c.ClientSecretFile) != ""

	if hasSecret && hasSecretFile {
		return fmt.Errorf(
			"\"clientSecret\" and \"clientSecretFile\" are mutually exclusive",
		)
	}
	if !hasSecret && !hasSecretFile {
		return fmt.Errorf(
			"one of \"clientSecret\" or \"clientSecretFile\" must be set",
		)
	}

	if err := validateTokenURL(c.TokenURL); err != nil {
		return err
	}

	for index, scope := range c.Scopes {
		if strings.TrimSpace(scope) == "" {
			return fmt.Errorf("scope at index %d must not be empty", index)
		}
	}

	// Stable traversal makes errors deterministic when multiple endpoint
	// parameters contain invalid names.
	paramNames := make([]string, 0, len(c.EndpointParams))
	for name := range c.EndpointParams {
		paramNames = append(paramNames, name)
	}
	sort.Strings(paramNames)

	for _, name := range paramNames {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return fmt.Errorf("endpoint parameter name must not be empty")
		}

		switch strings.ToLower(trimmed) {
		case "client_id", "client_secret", "grant_type", "scope":
			return fmt.Errorf(
				"endpoint parameter %q is managed by the OAuth2 client",
				name,
			)
		}
	}

	if c.timeoutDuration() <= 0 {
		return fmt.Errorf("\"timeout\" must be greater than zero")
	}
	if c.expiryBufferDuration() < 0 {
		return fmt.Errorf("\"expiryBuffer\" must not be negative")
	}

	return nil
}

func validateTokenURL(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("missing required field \"tokenUrl\"")
	}

	parsed, err := url.Parse(value)
	if err != nil ||
		!parsed.IsAbs() ||
		parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf(
			"\"tokenUrl\" must be a valid absolute HTTP or HTTPS URL",
		)
	}

	if parsed.User != nil {
		return fmt.Errorf("\"tokenUrl\" must not contain user information")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("\"tokenUrl\" must not contain a fragment")
	}

	return nil
}

func (c *Config) timeoutDuration() time.Duration {
	if c.Timeout != nil {
		return time.Duration(*c.Timeout)
	}
	return defaultTimeout
}

func (c *Config) expiryBufferDuration() time.Duration {
	if c.ExpiryBuffer != nil {
		return time.Duration(*c.ExpiryBuffer)
	}
	return defaultExpiryBuffer
}
