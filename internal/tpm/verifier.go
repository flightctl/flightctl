package tpm

import (
	"crypto/x509"
	"fmt"
	"slices"
	"sync"
	"time"
)

var (
	ErrManufacturerCACertsNotConfigured = fmt.Errorf("TPM CA certificates not configured")
)

// CAPathProvider represents a function that returns a slice of CA file paths
type CAPathProvider func() ([]string, error)

// CAVerifier defines the interface required for verifying that a tpm based CSR is verified by expected
// manufacturer certs
type CAVerifier interface {
	VerifyChain(csrBytes []byte) error
}

type verifier struct {
	pathProvider    CAPathProvider
	mu              sync.RWMutex
	paths           []string
	certPool        *x509.CertPool
	lastReload      time.Time
	refreshInterval time.Duration
	poolGeneration  int64
}

// NewCAVerifier returns a CAVerifier that will refresh it's known cert pool when required
func NewCAVerifier(initialPaths []string, pathProvider CAPathProvider) CAVerifier {
	paths := slices.Clone(initialPaths)
	slices.Sort(paths)
	v := &verifier{
		pathProvider:    pathProvider,
		paths:           paths,
		refreshInterval: time.Minute,
	}

	if len(initialPaths) > 0 {
		if pool, err := LoadCAsFromPaths(initialPaths); err == nil {
			v.certPool = pool
		}
	}

	return v
}

// NewDisabledCAVerifier creates a verifier that always returns "not configured" error.
// This is useful for tests and scenarios where TPM verification is not needed.
func NewDisabledCAVerifier() CAVerifier {
	return &disabledVerifier{}
}

type disabledVerifier struct{}

func (d *disabledVerifier) VerifyChain(_ []byte) error {
	return ErrManufacturerCACertsNotConfigured
}

// VerifyChain performs validation on the supplied CSR to verify the CSR is genuine
func (v *verifier) VerifyChain(csrBytes []byte) error {
	v.mu.RLock()
	initialGeneration := v.poolGeneration
	pool := v.certPool
	v.mu.RUnlock()

	var err error
	if pool == nil {
		err = ErrManufacturerCACertsNotConfigured
	} else {
		err = VerifyTCGCSRChainOfTrustWithRoots(csrBytes, pool)
	}

	if err != nil {
		pool, reloadErr := v.reloadCertPool(initialGeneration)
		if reloadErr != nil {
			return reloadErr
		}
		if pool != nil {
			err = VerifyTCGCSRChainOfTrustWithRoots(csrBytes, pool)
		}
	}
	return err
}

func (v *verifier) reloadCertPool(generation int64) (*x509.CertPool, error) {
	if v.pathProvider == nil {
		return nil, fmt.Errorf("no path provider configured")
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	// a new pool has been generated, return the newest pool
	if generation < v.poolGeneration {
		return v.certPool, nil
	}

	// not allowed to reload yet
	if time.Since(v.lastReload) < v.refreshInterval {
		return nil, nil
	}

	v.lastReload = time.Now()

	paths, err := v.pathProvider()
	if err != nil {
		return nil, fmt.Errorf("getting CA paths: %w", err)
	}
	if len(paths) == 0 {
		return nil, ErrManufacturerCACertsNotConfigured
	}

	pathClone := slices.Clone(paths)

	// keep the paths sorted for easier comparison
	slices.Sort(pathClone)

	// Nothing changed, no need to reload
	if slices.Equal(v.paths, pathClone) && v.certPool != nil {
		return nil, nil
	}
	v.paths = pathClone
	newPool, err := LoadCAsFromPaths(paths)
	if err != nil {
		return nil, fmt.Errorf("loading CA certificates: %w", err)
	}

	v.certPool = newPool
	v.poolGeneration++
	return v.certPool, nil
}
