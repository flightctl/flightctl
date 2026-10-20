package service_test

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/service/tpmcsr"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	erstore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	"github.com/google/uuid"
)

type unavailableIssuancePersistence struct {
	csrstore.Store
}

func (persistence *unavailableIssuancePersistence) Mutate(ctx context.Context, orgID uuid.UUID, name string, previous *domain.CertificateSigningRequest, apply func(*store.Mutation[domain.CertificateSigningRequest]) error) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	return persistence.Store.Mutate(ctx, orgID, name, previous, func(mutation *store.Mutation[domain.CertificateSigningRequest]) error {
		if err := apply(mutation); err != nil {
			return err
		}
		resource := mutation.Resource()
		if resource.Status != nil && resource.Status.Certificate != nil && len(*resource.Status.Certificate) > 0 {
			return errors.New("issuance publication unavailable")
		}
		return nil
	})
}

type unavailableCSRReads struct {
	csrstore.Store
}

func (persistence *unavailableCSRReads) Get(context.Context, uuid.UUID, string) (*domain.CertificateSigningRequest, error) {
	return nil, errors.New("credential storage unavailable")
}

func (persistence *unavailableCSRReads) List(context.Context, uuid.UUID, store.ListParams) (*domain.CertificateSigningRequestList, error) {
	return nil, errors.New("credential storage unavailable")
}

type concurrentEnrollmentPersistence struct {
	erstore.Store
	concurrentWrite func() error
}

func (persistence *concurrentEnrollmentPersistence) Mutate(ctx context.Context, orgID uuid.UUID, name string, previous *domain.EnrollmentRequest, apply func(*store.Mutation[domain.EnrollmentRequest]) error) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, bool, error) {
	return persistence.Store.Mutate(ctx, orgID, name, previous, func(mutation *store.Mutation[domain.EnrollmentRequest]) error {
		if err := apply(mutation); err != nil {
			return err
		}
		write := persistence.concurrentWrite
		persistence.concurrentWrite = nil
		if write != nil {
			return write()
		}
		return nil
	})
}

func (suite *ServiceTestSuite) enrollmentServiceWithPersistence(csrPersistence csrstore.Store, erPersistence erstore.Store) enrollmentrequestservice.Service {
	eventService := events.NewServiceHandler(suite.EventStore, suite.workerClient, suite.Log)

	var tpmVerifier tpmcsr.Verifier
	csrSvc := csrservice.NewServiceHandler(csrPersistence, &tpmVerifier, suite.caClient, eventService, suite.Log, "", "")
	erSvc := enrollmentrequestservice.NewServiceHandler(erPersistence, suite.DeviceStore, csrSvc,
		suite.caClient, suite.kvStore, eventService, suite.Log, nil, "", "", suite.EnrollmentHookPolicy, suite.NotifySecretsStore)
	tpmVerifier = *tpmcsr.NewVerifier(erSvc)
	return erSvc
}
