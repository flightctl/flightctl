package config

import (
	"fmt"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/util/validation"
)

// DeltaGenerationConfig contains configuration used by the delta worker.
type DeltaGenerationConfig struct {
	DefaultRepository             *DefaultRepositoryConfig `json:"defaultRepository,omitempty"`
	MaxConcurrentDeltaGenerations int                      `json:"maxConcurrentDeltaGenerations,omitempty"`
	Timeout                       util.Duration            `json:"timeout,omitempty"`
	MaxWaitForDelta               *util.Duration           `json:"maxWaitForDelta,omitempty"`
}

const maxConcurrentDeltaGenerationsLimit = 32

// EffectiveMaxConcurrentDeltaGenerations returns the configured consumer count,
// defaulting to 2 and capped at maxConcurrentDeltaGenerationsLimit.
func (c *DeltaGenerationConfig) EffectiveMaxConcurrentDeltaGenerations() int {
	if c == nil || c.MaxConcurrentDeltaGenerations <= 0 {
		return 2
	}
	if c.MaxConcurrentDeltaGenerations > maxConcurrentDeltaGenerationsLimit {
		return maxConcurrentDeltaGenerationsLimit
	}
	return c.MaxConcurrentDeltaGenerations
}

func (c *DeltaGenerationConfig) EffectiveTimeout() time.Duration {
	if c == nil || time.Duration(c.Timeout) <= 0 {
		return 30 * time.Minute
	}
	return time.Duration(c.Timeout)
}

func (c *DeltaGenerationConfig) EffectiveMaxWaitForDelta() *time.Duration {
	if c == nil || c.MaxWaitForDelta == nil {
		return nil
	}
	d := time.Duration(*c.MaxWaitForDelta)
	return &d
}

// Validate validates the default OCI repository settings.
func (c *DeltaGenerationConfig) Validate() error {
	if c == nil || c.DefaultRepository == nil {
		return nil
	}
	d := c.DefaultRepository
	repoSet := d.Repository != nil && strings.TrimSpace(*d.Repository) != ""
	nsSet := d.Namespace != nil && strings.TrimSpace(*d.Namespace) != ""
	schemeSet := d.Scheme != nil && strings.TrimSpace(*d.Scheme) != ""
	caSet := d.CaCrt != nil && strings.TrimSpace(*d.CaCrt) != ""
	skipSet := d.SkipServerVerification != nil
	credsSet := d.Username != "" || d.Password != ""
	anySet := strings.TrimSpace(d.Registry) != "" || repoSet || nsSet || schemeSet || caSet || skipSet || credsSet
	if anySet {
		if errs := validation.ValidateHostIPOrFQDNWithOptionalPort(&d.Registry, "deltaGeneration.defaultRepository.registry"); len(errs) > 0 {
			return errs[0]
		}
	}
	if repoSet && nsSet {
		return fmt.Errorf("deltaGeneration.defaultRepository.repository and namespace are mutually exclusive")
	}
	if d.Scheme != nil && *d.Scheme != "" && *d.Scheme != "http" && *d.Scheme != "https" {
		return fmt.Errorf("deltaGeneration.defaultRepository.scheme must be http or https")
	}
	if d.Scheme != nil && *d.Scheme == "http" && credsSet {
		return fmt.Errorf("deltaGeneration.defaultRepository cannot use credentials with http")
	}
	if repoSet {
		if errs := validation.ValidateString(d.Repository, "deltaGeneration.defaultRepository.repository", 1, 255, validation.OciImageNameRegexp, validation.OciImageNameFmt); len(errs) > 0 {
			return errs[0]
		}
	}
	if nsSet {
		if errs := validation.ValidateString(d.Namespace, "deltaGeneration.defaultRepository.namespace", 1, 255, validation.OciImageNameRegexp, validation.OciImageNameFmt); len(errs) > 0 {
			return errs[0]
		}
	}
	return nil
}

// DefaultRepositoryConfig configures the OCI repository used for generated deltas.
type DefaultRepositoryConfig struct {
	Registry               string           `json:"registry,omitempty"`
	Repository             *string          `json:"repository,omitempty"`
	Namespace              *string          `json:"namespace,omitempty"`
	Scheme                 *string          `json:"scheme,omitempty"`
	SkipServerVerification *bool            `json:"skipServerVerification,omitempty"`
	CaCrt                  *string          `json:"ca.crt,omitempty"`
	Username               string           `json:"-"`
	Password               api.SecureString `json:"-"`
}

func (d *DefaultRepositoryConfig) OciRepoSpec() (*domain.OciRepoSpec, error) {
	if d == nil || d.Registry == "" {
		return nil, nil
	}
	accessMode := domain.OciRepoAccessModeReadWrite
	spec := &domain.OciRepoSpec{
		Type:                   domain.OciRepoSpecTypeOci,
		Registry:               d.Registry,
		Repository:             d.Repository,
		Namespace:              d.Namespace,
		AccessMode:             &accessMode,
		SkipServerVerification: d.SkipServerVerification,
		CaCrt:                  d.CaCrt,
	}
	if d.Scheme != nil && *d.Scheme != "" {
		scheme := domain.OciRepoSpecScheme(*d.Scheme)
		spec.Scheme = &scheme
	}
	if d.Username == "" || d.Password == "" {
		return spec, nil
	}
	auth := &domain.OciAuth{}
	if err := auth.FromDockerAuth(domain.DockerAuth{
		Username: d.Username,
		Password: string(d.Password),
	}); err != nil {
		return nil, fmt.Errorf("default repository authentication: %w", err)
	}
	spec.OciAuth = auth
	return spec, nil
}
