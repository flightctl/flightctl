package service_test

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/service/tpmcsr"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// seedHistoricalCSRStatus injects retained pre-feature issuance or a controlled
// concurrent winner. Current handlers deliberately cannot rewrite this pair.
func seedHistoricalCSRStatus(provider csrstore.Store, ctx context.Context, orgID uuid.UUID, candidate *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	updated, _, _, err := provider.Mutate(ctx, orgID, *candidate.Metadata.Name, nil, func(mutation *store.Mutation[domain.CertificateSigningRequest]) error {
		if err := mutation.RequireExisting(); err != nil {
			return err
		}
		mutation.Resource().Status = candidate.Status
		return nil
	})
	return updated, err
}

// seedPreFeatureCertificate represents retained certificates that current signing
// cannot create: missing identity metadata, unknown time, or historical signers.
func seedPreFeatureCertificate(provider csrstore.Store, ctx context.Context, orgID uuid.UUID, request domain.CertificateSigningRequest, certificate []byte, issuedAt *time.Time) (*domain.CertificateSigningRequest, error) {
	created, err := provider.Create(ctx, orgID, &request)
	if err != nil {
		return nil, err
	}
	created.Status = &domain.CertificateSigningRequestStatus{IssuedAt: issuedAt}
	if certificate != nil {
		created.Status.Certificate = &certificate
	}
	return seedHistoricalCSRStatus(provider, ctx, orgID, created)
}

// createUnpublishedCSR uses the real handler with unavailable publication so later
// approvals can exercise first issuance without bypassing creation policy.
func (suite *ServiceTestSuite) createUnpublishedCSR(provider csrstore.Store, ctx context.Context, orgID uuid.UUID, request domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	service := suite.csrServiceWithSigner(&unavailableIssuancePersistence{Store: provider}, request.Spec.SignerName)
	created, status := service.CreateCertificateSigningRequest(ctx, orgID, request)
	if status.Code != 201 {
		return nil, fmt.Errorf("creation status %d: %s", status.Code, status.Message)
	}
	return created, nil
}

// replaceHistoricalCSRSigner models an old row whose signer has changed to a
// management signer that the current CSR API intentionally refuses to create.
func replaceHistoricalCSRSigner(provider csrstore.Store, ctx context.Context, orgID uuid.UUID, candidate *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	updated, _, err := provider.Update(ctx, orgID, candidate)
	return updated, err
}

// injectCertificateDigestCollision tests that the private index is not identity
// proof. Normal winning writes always derive the digest from the actual leaf.
func injectCertificateDigestCollision(db *gorm.DB, orgID uuid.UUID, name, digest string) error {
	result := db.Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", orgID, name).Update("certificate_digest", digest)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("collision fixture updated %d rows", result.RowsAffected)
	}
	return nil
}

// corruptRetainedCertificateEncoding creates an operational JSON decoding error,
// not malformed PEM; typed service and store writes cannot encode invalid base64.
func corruptRetainedCertificateEncoding(db *gorm.DB, orgID uuid.UUID, name string) error {
	result := db.Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", orgID, name).Update("status", model.MakeJSONField(map[string]any{"certificate": "!bad-base64!"}))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("encoding fixture updated %d rows", result.RowsAffected)
	}
	return nil
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

// concurrentCSRPublication injects a database interleaving after handler policy
// has prepared its candidate, without replacing store persistence or CAS logic.
type concurrentCSRPublication struct {
	csrstore.Store
	concurrentWrite func(*domain.CertificateSigningRequest) error
}

func (provider *concurrentCSRPublication) Mutate(ctx context.Context, orgID uuid.UUID, name string, previous *domain.CertificateSigningRequest, apply func(*store.Mutation[domain.CertificateSigningRequest]) error) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	return provider.Store.Mutate(ctx, orgID, name, previous, func(mutation *store.Mutation[domain.CertificateSigningRequest]) error {
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
