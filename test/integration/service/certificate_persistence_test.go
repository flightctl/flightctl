package service_test

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/service/tpmcsr"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/google/uuid"
)

func seedCSRStatus(provider csrstore.Store, ctx context.Context, orgID uuid.UUID, candidate *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	updated, _, _, err := provider.Mutate(ctx, orgID, *candidate.Metadata.Name, func(mutation *store.Mutation[domain.CertificateSigningRequest]) error {
		if err := mutation.RequireExisting(); err != nil {
			return err
		}
		mutation.Resource().Status = candidate.Status
		return nil
	})
	return updated, err
}

func (suite *ServiceTestSuite) csrServiceWithSigner(provider csrstore.Store, signerName string) csrservice.Service {
	caClient := *suite.caClient
	cfg := *caClient.Cfg
	cfg.DeviceEnrollmentSignerName = signerName
	caClient.Cfg = &cfg
	return csrservice.NewServiceHandler(provider, tpmcsr.NewVerifier(suite.EnrollmentRequest), &caClient,
		events.NewServiceHandler(suite.EventStore, suite.workerClient, suite.Log), suite.Log, "", "")
}

func approveCSRConditions(service csrservice.Service, ctx context.Context, orgID uuid.UUID, candidate *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	updated, status := service.UpdateCertificateSigningRequestApproval(ctx, orgID, *candidate.Metadata.Name, *candidate)
	if status.Code != 200 {
		return nil, fmt.Errorf("approval status %d: %s", status.Code, status.Message)
	}
	return updated, nil
}

type concurrentCSRPublication struct {
	csrstore.Store
	concurrentWrite func(*domain.CertificateSigningRequest) error
}

func (provider *concurrentCSRPublication) Mutate(ctx context.Context, orgID uuid.UUID, name string, apply func(*store.Mutation[domain.CertificateSigningRequest]) error) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	return provider.Store.Mutate(ctx, orgID, name, func(mutation *store.Mutation[domain.CertificateSigningRequest]) error {
		if err := apply(mutation); err != nil {
			return err
		}
		candidate := mutation.Resource()
		if candidate.Status != nil && candidate.Status.Certificate != nil && provider.concurrentWrite != nil {
			write := provider.concurrentWrite
			provider.concurrentWrite = nil
			return write(candidate)
		}
		return nil
	})
}
