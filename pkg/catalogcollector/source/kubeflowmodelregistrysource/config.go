package kubeflowmodelregistrysource

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/util/validation"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/source/pollsource"
)

const (
	defaultPageSize          = 100
	defaultPollInterval      = 5 * time.Minute
	defaultRequestTimeout    = 30 * time.Second
	defaultCollectionTimeout = 5 * time.Minute
)

// AuthConfig holds a reference to an authentication extension used by this
// source.
type AuthConfig struct {
	// Authenticator is the component ID of the extension that provides HTTP
	// authentication, for example "bearertokenauth/model-registry".
	Authenticator string `json:"authenticator"`
}

// Config holds the provider-specific configuration for the Kubeflow Model
// Registry source. It implements config.Validator.
type Config struct {
	// Endpoint is the absolute HTTP or HTTPS base URL of the Model Registry
	// REST API, for example
	// "https://model-registry-rhoai-rest.apps.example.com".
	Endpoint string `json:"endpoint"`

	// Catalog is the name of the target Flightctl Catalog resource.
	Catalog string `json:"catalog"`

	// PollInterval is the wait between successive successful collection
	// cycles. Defaults to 5m.
	PollInterval *util.Duration `json:"pollInterval,omitempty"`

	// PageSize is the number of items requested per paginated API call.
	// Zero selects the default of 100.
	PageSize int `json:"pageSize,omitempty"`

	// RequestTimeout bounds a single HTTP request. Defaults to 30s.
	RequestTimeout *util.Duration `json:"requestTimeout,omitempty"`

	// CollectionTimeout bounds an entire collection cycle, including all
	// pagination and normalization. Defaults to 5m.
	CollectionTimeout *util.Duration `json:"collectionTimeout,omitempty"`

	// Auth optionally references an authentication extension. When omitted,
	// requests are sent without authentication.
	Auth *AuthConfig `json:"auth,omitempty"`

	// CertificateAuthority is the path to a PEM CA bundle used to verify the
	// registry TLS certificate. System roots are used when omitted.
	CertificateAuthority string `json:"certificateAuthority,omitempty"`

	// InsecureSkipVerify disables TLS certificate verification.
	// This option is intended for development only.
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`

	// Backoff holds the bounded exponential backoff parameters used after
	// failed collection or downstream-consumption attempts.
	Backoff pollsource.BackoffConfig `json:"backoff,omitempty"`
}

// Validate implements config.Validator.
func (c *Config) Validate() error {
	if err := validateEndpoint(c.Endpoint); err != nil {
		return err
	}

	if err := validateCatalogName(c.Catalog); err != nil {
		return err
	}

	if c.PollInterval != nil {
		value := time.Duration(*c.PollInterval)
		if value <= 0 {
			return fmt.Errorf(
				"pollInterval must be positive when set, got %s",
				value,
			)
		}
	}

	if c.PageSize < 0 {
		return fmt.Errorf(
			"pageSize must be non-negative, got %d",
			c.PageSize,
		)
	}
	if c.PageSize > math.MaxInt32 {
		return fmt.Errorf(
			"pageSize must not exceed %d, got %d",
			math.MaxInt32,
			c.PageSize,
		)
	}

	if c.RequestTimeout != nil {
		value := time.Duration(*c.RequestTimeout)
		if value <= 0 {
			return fmt.Errorf(
				"requestTimeout must be positive when set, got %s",
				value,
			)
		}
	}

	if c.CollectionTimeout != nil {
		value := time.Duration(*c.CollectionTimeout)
		if value <= 0 {
			return fmt.Errorf(
				"collectionTimeout must be positive when set, got %s",
				value,
			)
		}
	}

	if c.Auth != nil {
		authenticator := strings.TrimSpace(c.Auth.Authenticator)
		if authenticator == "" {
			return fmt.Errorf(
				"auth.authenticator must not be empty when auth is configured",
			)
		}
		if authenticator != c.Auth.Authenticator {
			return fmt.Errorf(
				"auth.authenticator must not contain leading or trailing whitespace",
			)
		}
		if _, err := catalogcollector.ParseComponentID(authenticator); err != nil {
			return fmt.Errorf(
				"auth.authenticator must be a valid component ID: %w",
				err,
			)
		}
	}

	if err := c.Backoff.Validate(); err != nil {
		return fmt.Errorf("backoff: %w", err)
	}

	return nil
}

func validateEndpoint(endpoint string) error {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return fmt.Errorf("missing required field \"endpoint\"")
	}
	if trimmed != endpoint {
		return fmt.Errorf(
			"endpoint must not contain leading or trailing whitespace",
		)
	}

	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf(
			"endpoint must be an absolute HTTP or HTTPS URL",
		)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf(
			"endpoint scheme must be \"http\" or \"https\"",
		)
	}

	if parsed.User != nil {
		return fmt.Errorf(
			"endpoint must not contain user information; configure authentication through auth.authenticator",
		)
	}
	if parsed.RawQuery != "" {
		return fmt.Errorf("endpoint must not contain query parameters")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("endpoint must not contain a fragment")
	}

	return nil
}

func validateCatalogName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("missing required field \"catalog\"")
	}
	if trimmed != name {
		return fmt.Errorf(
			"catalog must not contain leading or trailing whitespace",
		)
	}

	validationErrors := validation.ValidateResourceName(&name)
	if len(validationErrors) != 0 {
		return fmt.Errorf(
			"catalog must be a valid Flightctl resource name: %w",
			errors.Join(validationErrors...),
		)
	}

	return nil
}

func (c *Config) pollInterval() time.Duration {
	if c.PollInterval != nil {
		return time.Duration(*c.PollInterval)
	}
	return defaultPollInterval
}

func (c *Config) requestTimeout() time.Duration {
	if c.RequestTimeout != nil {
		return time.Duration(*c.RequestTimeout)
	}
	return defaultRequestTimeout
}

func (c *Config) collectionTimeout() time.Duration {
	if c.CollectionTimeout != nil {
		return time.Duration(*c.CollectionTimeout)
	}
	return defaultCollectionTimeout
}

func (c *Config) pageSize() int {
	if c.PageSize > 0 {
		return c.PageSize
	}
	return defaultPageSize
}
