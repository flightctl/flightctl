package enrollmenthookpolicy

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Store interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, policy *domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, error)
	Update(ctx context.Context, orgId uuid.UUID, policy *domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, *domain.EnrollmentHookPolicy, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, policy *domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, *domain.EnrollmentHookPolicy, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentHookPolicy, error)
	List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EnrollmentHookPolicyList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string) (bool, error)
}

type EnrollmentHookPolicyStore struct {
	dbHandler    *gorm.DB
	log          logrus.FieldLogger
	genericStore *store.GenericStore[*model.EnrollmentHookPolicy, model.EnrollmentHookPolicy, domain.EnrollmentHookPolicy, domain.EnrollmentHookPolicyList]
}

var _ Store = (*EnrollmentHookPolicyStore)(nil)

func NewStore(db *gorm.DB, log logrus.FieldLogger) Store {
	genericStore := store.NewGenericStore[*model.EnrollmentHookPolicy, model.EnrollmentHookPolicy, domain.EnrollmentHookPolicy, domain.EnrollmentHookPolicyList](
		db,
		log,
		model.NewEnrollmentHookPolicyFromApiResource,
		(*model.EnrollmentHookPolicy).ToApiResource,
		model.EnrollmentHookPoliciesToApiResource,
	)
	return &EnrollmentHookPolicyStore{dbHandler: db, log: log, genericStore: genericStore}
}

func (s *EnrollmentHookPolicyStore) InitialMigration(ctx context.Context) error {
	db := s.dbHandler.WithContext(ctx)

	if err := db.AutoMigrate(&model.EnrollmentHookPolicy{}); err != nil {
		return err
	}

	if !db.Migrator().HasIndex(&model.EnrollmentHookPolicy{}, "idx_enrollment_hook_policies_labels") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_enrollment_hook_policies_labels ON enrollment_hook_policies USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.EnrollmentHookPolicy{}, "Labels"); err != nil {
				return err
			}
		}
	}

	if !db.Migrator().HasIndex(&model.EnrollmentHookPolicy{}, "idx_enrollment_hook_policies_annotations") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_enrollment_hook_policies_annotations ON enrollment_hook_policies USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.EnrollmentHookPolicy{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *EnrollmentHookPolicyStore) Create(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, error) {
	return s.genericStore.Create(ctx, orgId, resource)
}

func (s *EnrollmentHookPolicyStore) Update(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, *domain.EnrollmentHookPolicy, error) {
	return s.genericStore.Update(ctx, orgId, resource, nil, nil)
}

func (s *EnrollmentHookPolicyStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, *domain.EnrollmentHookPolicy, bool, error) {
	return s.genericStore.CreateOrUpdate(ctx, orgId, resource, nil, nil)
}

func (s *EnrollmentHookPolicyStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentHookPolicy, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *EnrollmentHookPolicyStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EnrollmentHookPolicyList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *EnrollmentHookPolicyStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (bool, error) {
	return s.genericStore.Delete(ctx, model.EnrollmentHookPolicy{Resource: model.Resource{OrgID: orgId, Name: name}})
}
