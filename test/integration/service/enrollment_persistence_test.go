package service_test

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	erstore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	"github.com/google/uuid"
)

type unavailableIssuancePersistence struct {
	csrstore.Store
}

func (persistence *unavailableIssuancePersistence) UpdateStatus(ctx context.Context, orgID uuid.UUID, resource *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	if resource.Status != nil && resource.Status.Certificate != nil && len(*resource.Status.Certificate) > 0 {
		return nil, errors.New("issuance publication unavailable")
	}
	return persistence.Store.UpdateStatus(ctx, orgID, resource)
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

func (persistence *concurrentEnrollmentPersistence) Get(ctx context.Context, orgID uuid.UUID, name string) (*domain.EnrollmentRequest, error) {
	snapshot, err := persistence.Store.Get(ctx, orgID, name)
	write := persistence.concurrentWrite
	persistence.concurrentWrite = nil
	if write != nil {
		if writeErr := write(); writeErr != nil {
			return nil, writeErr
		}
	}
	return snapshot, err
}

func (suite *ServiceTestSuite) enrollmentServiceWithPersistence(csrPersistence csrstore.Store, erPersistence erstore.Store) enrollmentrequestservice.Service {
	eventService := events.NewServiceHandler(suite.EventStore, suite.workerClient, suite.Log)
	return enrollmentrequestservice.NewServiceHandler(erPersistence, suite.DeviceStore, csrPersistence,
		suite.caClient, suite.kvStore, eventService, suite.Log, nil, "", "", suite.EnrollmentHookPolicy, suite.NotifySecretsStore)
}
