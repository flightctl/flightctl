package config

import (
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/util"
)

// DeltaGenerationConfig contains configuration used by the delta worker.
type DeltaGenerationConfig struct {
	DefaultRepository             *DefaultRepositoryConfig `json:"defaultRepository,omitempty"`
	MaxConcurrentDeltaGenerations int                      `json:"maxConcurrentDeltaGenerations,omitempty"`
	Timeout                       util.Duration            `json:"timeout,omitempty"`
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

// EffectiveTimeout returns the configured generation timeout or its default.
func (c *DeltaGenerationConfig) EffectiveTimeout() time.Duration {
	if c == nil || time.Duration(c.Timeout) <= 0 {
		return 30 * time.Minute
	}
	return time.Duration(c.Timeout)
}

// DefaultRepositoryConfig configures the default OCI write target.
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

// OciRepoSpec converts the default repository to an OCI repository spec.
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
