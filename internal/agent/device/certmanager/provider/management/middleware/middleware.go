package middleware

import (
	"context"
	"crypto/x509"
	"fmt"

	"github.com/flightctl/flightctl/pkg/certmanager"
)

// provisionMiddleware allows wrapping a Provision call.
// If nil, the call is forwarded directly to next.
type provisionMiddleware func(ctx context.Context, next certmanager.ProvisionerProvider, req certmanager.ProvisionRequest) (*certmanager.ProvisionResult, error)

type chainProvisioner struct {
	next      certmanager.ProvisionerProvider
	provision provisionMiddleware
}

func (p *chainProvisioner) Provision(ctx context.Context, req certmanager.ProvisionRequest) (*certmanager.ProvisionResult, error) {
	if p.next == nil {
		return nil, fmt.Errorf("nil next provisioner")
	}
	if p.provision != nil {
		return p.provision(ctx, p.next, req)
	}
	return p.next.Provision(ctx, req)
}

type chainProvisionerFactory struct {
	next      certmanager.ProvisionerFactory
	provision provisionMiddleware
}

func (f *chainProvisionerFactory) Type() string {
	if f.next == nil {
		return "management/chain"
	}
	return f.next.Type()
}

func (f *chainProvisionerFactory) Validate(log certmanager.Logger, cc certmanager.CertificateConfig) error {
	if f.next == nil {
		return fmt.Errorf("nil next provisioner factory")
	}
	return f.next.Validate(log, cc)
}

func (f *chainProvisionerFactory) New(log certmanager.Logger, cc certmanager.CertificateConfig) (certmanager.ProvisionerProvider, error) {
	if f.next == nil {
		return nil, fmt.Errorf("nil next provisioner factory")
	}

	p, err := f.next.New(log, cc)
	if err != nil {
		return nil, err
	}

	return &chainProvisioner{
		next:      p,
		provision: f.provision,
	}, nil
}

// storeMiddleware allows wrapping a Store call.
// If nil, the call is forwarded directly to next.
type storeMiddleware func(ctx context.Context, next certmanager.StorageProvider, req certmanager.StoreRequest) error

// loadCertMiddleware allows wrapping a LoadCertificate call.
// If nil, the call is forwarded directly to next.
type loadCertMiddleware func(ctx context.Context, next certmanager.StorageProvider) (*x509.Certificate, error)

type chainStorage struct {
	next     certmanager.StorageProvider
	store    storeMiddleware
	loadCert loadCertMiddleware
}

func (s *chainStorage) Store(ctx context.Context, req certmanager.StoreRequest) error {
	if s.next == nil {
		return fmt.Errorf("nil next storage")
	}
	if s.store != nil {
		return s.store(ctx, s.next, req)
	}
	return s.next.Store(ctx, req)
}

func (s *chainStorage) LoadCertificate(ctx context.Context) (*x509.Certificate, error) {
	if s.next == nil {
		return nil, fmt.Errorf("nil next storage")
	}
	if s.loadCert != nil {
		return s.loadCert(ctx, s.next)
	}
	return s.next.LoadCertificate(ctx)
}

type chainStorageFactory struct {
	next     certmanager.StorageFactory
	store    storeMiddleware
	loadCert loadCertMiddleware
}

func (f *chainStorageFactory) Type() string {
	if f.next == nil {
		return "management/chain"
	}
	return f.next.Type()
}

func (f *chainStorageFactory) Validate(log certmanager.Logger, cc certmanager.CertificateConfig) error {
	if f.next == nil {
		return fmt.Errorf("nil next storage factory")
	}
	return f.next.Validate(log, cc)
}

func (f *chainStorageFactory) New(log certmanager.Logger, cc certmanager.CertificateConfig) (certmanager.StorageProvider, error) {
	if f.next == nil {
		return nil, fmt.Errorf("nil next storage factory")
	}

	s, err := f.next.New(log, cc)
	if err != nil {
		return nil, err
	}

	return &chainStorage{
		next:     s,
		store:    f.store,
		loadCert: f.loadCert,
	}, nil
}
