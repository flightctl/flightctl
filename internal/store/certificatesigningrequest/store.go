package certificatesigningrequest

import (
	"context"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Store interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, req *domain.CertificateSigningRequest, eventCallback store.EventCallback) (*domain.CertificateSigningRequest, error)
	Update(ctx context.Context, orgId uuid.UUID, req *domain.CertificateSigningRequest, eventCallback store.EventCallback) (*domain.CertificateSigningRequest, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, certificatesigningrequest *domain.CertificateSigningRequest, eventCallback store.EventCallback) (*domain.CertificateSigningRequest, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, error)
	List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.CertificateSigningRequestList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, certificatesigningrequest *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error)

	UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) error
}

type CertificateSigningRequestStore struct {
	dbHandler           *gorm.DB
	log                 logrus.FieldLogger
	genericStore        *store.GenericStore[*model.CertificateSigningRequest, model.CertificateSigningRequest, domain.CertificateSigningRequest, domain.CertificateSigningRequestList]
	eventCallbackCaller store.EventCallbackCaller
}

// Make sure we conform to the Store interface
var _ Store = (*CertificateSigningRequestStore)(nil)

func NewCertificateSigningRequestStore(db *gorm.DB, log logrus.FieldLogger) Store {
	genericStore := store.NewGenericStore[*model.CertificateSigningRequest, model.CertificateSigningRequest, domain.CertificateSigningRequest, domain.CertificateSigningRequestList](
		db,
		log,
		model.NewCertificateSigningRequestFromApiResource,
		(*model.CertificateSigningRequest).ToApiResource,
		model.CertificateSigningRequestsToApiResource,
	)
	return &CertificateSigningRequestStore{dbHandler: db, log: log, genericStore: genericStore, eventCallbackCaller: store.CallEventCallback(domain.CertificateSigningRequestKind, log)}
}

func (s *CertificateSigningRequestStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *CertificateSigningRequestStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)

	if err := db.AutoMigrate(&model.CertificateSigningRequest{}); err != nil {
		return err
	}

	// Create GIN index for CertificateSigningRequest labels
	if !db.Migrator().HasIndex(&model.CertificateSigningRequest{}, "idx_csr_labels") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_csr_labels ON certificate_signing_requests USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.CertificateSigningRequest{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for CertificateSigningRequest annotations
	if !db.Migrator().HasIndex(&model.CertificateSigningRequest{}, "idx_csr_annotations") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_csr_annotations ON certificate_signing_requests USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.CertificateSigningRequest{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

// Warning: this is a user-facing function and will set the Status to nil
func (s *CertificateSigningRequestStore) Create(ctx context.Context, orgId uuid.UUID, resource *domain.CertificateSigningRequest, eventCallback store.EventCallback) (*domain.CertificateSigningRequest, error) {
	csr, err := s.genericStore.Create(ctx, orgId, resource)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), nil, csr, true, err)
	return csr, err
}

// Warning: this is a user-facing function and will set the Status to nil
func (s *CertificateSigningRequestStore) Update(ctx context.Context, orgId uuid.UUID, resource *domain.CertificateSigningRequest, eventCallback store.EventCallback) (*domain.CertificateSigningRequest, error) {
	newCsr, oldCsr, err := s.genericStore.Update(ctx, orgId, resource, nil, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldCsr, newCsr, false, err)
	return newCsr, err
}

func (s *CertificateSigningRequestStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *domain.CertificateSigningRequest, eventCallback store.EventCallback) (*domain.CertificateSigningRequest, bool, error) {
	newCsr, oldCsr, created, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, nil)
	s.eventCallbackCaller(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldCsr, newCsr, created, err)
	return newCsr, created, err
}

func (s *CertificateSigningRequestStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *CertificateSigningRequestStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.CertificateSigningRequestList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *CertificateSigningRequestStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error {
	deleted, err := s.genericStore.Delete(ctx, model.CertificateSigningRequest{Resource: model.Resource{OrgID: orgId, Name: name}})
	if deleted {
		s.eventCallbackCaller(ctx, eventCallback, orgId, name, nil, nil, false, err)
	}
	return err
}

func (s *CertificateSigningRequestStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	return s.genericStore.UpdateStatus(ctx, orgId, resource)
}

func (s *CertificateSigningRequestStore) updateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) (bool, error) {
	existingRecord := model.CertificateSigningRequest{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.getDB(ctx).Take(&existingRecord)
	if result.Error != nil {
		return false, store.ErrorFromGormError(result.Error)
	}

	if existingRecord.Status == nil {
		existingRecord.Status = model.MakeJSONField(domain.CertificateSigningRequestStatus{})
	}
	if existingRecord.Status.Data.Conditions == nil {
		existingRecord.Status.Data.Conditions = []domain.Condition{}
	}
	changed := false
	for _, condition := range conditions {
		changed = domain.SetStatusCondition(&existingRecord.Status.Data.Conditions, condition)
	}
	if !changed {
		return false, nil
	}

	result = s.getDB(ctx).Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"status":           existingRecord.Status,
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	err := store.ErrorFromGormError(result.Error)
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), err
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *CertificateSigningRequestStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) error {
	return retryUpdate(func() (bool, error) {
		return s.updateConditions(ctx, orgId, name, conditions)
	})
}

// retryIterations and retryUpdate mirror the unexported helpers of the same
// name in internal/store (internal/store/common.go), which are not exported
// for cross-package reuse. Duplicated here since UpdateConditions needs
// identical optimistic-concurrency retry behavior within this package.
const retryIterations = 10

func retryUpdate(fn func() (bool, error)) error {
	var (
		retry bool
		err   error
	)
	i := 0
	for retry, err = fn(); retry && i < retryIterations; retry, err = fn() {
		i++
	}
	return err
}
