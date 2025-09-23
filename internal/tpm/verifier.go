package tpm

import (
	"context"
	"crypto/x509"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
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
	log             *logrus.Logger
	mu              sync.Mutex
	paths           []string
	certPool        *x509.CertPool
	refreshInterval time.Duration
	ctx             context.Context
}

// NewCAVerifier returns a CAVerifier that will refresh it's known cert pool when required
func NewCAVerifier(ctx context.Context, initialPaths []string, pathProvider CAPathProvider, logger *logrus.Logger) CAVerifier {
	paths := slices.Clone(initialPaths)
	// keep paths sorted for easy comparison
	slices.Sort(paths)

	v := &verifier{
		pathProvider:    pathProvider,
		log:             logger,
		paths:           paths,
		refreshInterval: time.Minute,
		ctx:             ctx,
	}

	if len(initialPaths) > 0 {
		if pool, err := LoadCAsFromPaths(initialPaths); err == nil {
			v.certPool = pool
		}
	}

	go v.periodicReload()

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

// periodicReload runs in the background and reloads certificates at regular intervals
func (v *verifier) periodicReload() {
	ticker := time.NewTicker(v.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-v.ctx.Done():
			return
		case <-ticker.C:
			if err := v.reloadCertPool(); err != nil {
				v.log.Errorf("Error reloading certificate pool: %v", err)
			}
		}
	}
}

// VerifyChain performs validation on the supplied CSR to verify the CSR is genuine
func (v *verifier) VerifyChain(csrBytes []byte) error {
	v.mu.Lock()
	pool := v.certPool
	v.mu.Unlock()

	if pool == nil {
		return ErrManufacturerCACertsNotConfigured
	}

	return VerifyTCGCSRChainOfTrustWithRoots(csrBytes, pool)
}

func (v *verifier) reloadCertPool() error {
	if v.pathProvider == nil {
		return fmt.Errorf("no path provider configured")
	}

	paths, err := v.pathProvider()
	if err != nil {
		return fmt.Errorf("getting CA paths: %w", err)
	}
	if len(paths) == 0 {
		return ErrManufacturerCACertsNotConfigured
	}

	pathClone := slices.Clone(paths)

	// keep the paths sorted for easier comparison
	slices.Sort(pathClone)

	// Nothing changed, no need to reload
	if slices.Equal(v.paths, pathClone) && v.certPool != nil {
		return nil
	}

	v.paths = pathClone
	newPool, err := LoadCAsFromPaths(v.paths)
	if err != nil {
		return fmt.Errorf("loading CA certificates: %w", err)
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.certPool = newPool
	return nil
}
