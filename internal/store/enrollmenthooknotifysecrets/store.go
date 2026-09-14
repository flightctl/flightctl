package enrollmenthooknotifysecrets

import (
	"context"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

//go:generate mockgen -source=store.go -destination=mock_store.go -package=enrollmenthooknotifysecrets

// Store manages enrollment hook notify secrets.
type Store interface {
	InitialMigration(ctx context.Context) error
	Create(ctx context.Context, orgId uuid.UUID, secret *model.EnrollmentHookNotifySecret) error
	CreateBatch(ctx context.Context, orgId uuid.UUID, secrets []model.EnrollmentHookNotifySecret) error
	Get(ctx context.Context, orgId uuid.UUID, deviceName string, actionIndex int) (*model.EnrollmentHookNotifySecret, error)
	ListByDevice(ctx context.Context, orgId uuid.UUID, deviceName string) ([]model.EnrollmentHookNotifySecret, error)
	PurgeByDevice(ctx context.Context, orgId uuid.UUID, deviceName string) error
}

type storeImpl struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

func NewStore(db *gorm.DB, log logrus.FieldLogger) Store {
	return &storeImpl{db: db, log: log}
}

func (s *storeImpl) InitialMigration(ctx context.Context) error {
	return s.db.WithContext(ctx).AutoMigrate(&model.EnrollmentHookNotifySecret{})
}

func (s *storeImpl) Create(ctx context.Context, orgId uuid.UUID, secret *model.EnrollmentHookNotifySecret) error {
	secret.OrgID = orgId
	result := s.db.WithContext(ctx).Create(secret)
	return store.ErrorFromGormError(result.Error)
}

func (s *storeImpl) CreateBatch(ctx context.Context, orgId uuid.UUID, secrets []model.EnrollmentHookNotifySecret) error {
	if len(secrets) == 0 {
		return nil
	}
	for i := range secrets {
		secrets[i].OrgID = orgId
	}
	result := s.db.WithContext(ctx).Create(&secrets)
	return store.ErrorFromGormError(result.Error)
}

func (s *storeImpl) Get(ctx context.Context, orgId uuid.UUID, deviceName string, actionIndex int) (*model.EnrollmentHookNotifySecret, error) {
	var secret model.EnrollmentHookNotifySecret
	result := s.db.WithContext(ctx).
		Where("org_id = ? AND device_name = ? AND action_index = ?", orgId, deviceName, actionIndex).
		First(&secret)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return &secret, nil
}

func (s *storeImpl) ListByDevice(ctx context.Context, orgId uuid.UUID, deviceName string) ([]model.EnrollmentHookNotifySecret, error) {
	var secrets []model.EnrollmentHookNotifySecret
	result := s.db.WithContext(ctx).
		Where("org_id = ? AND device_name = ?", orgId, deviceName).
		Order("action_index ASC").
		Find(&secrets)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return secrets, nil
}

func (s *storeImpl) PurgeByDevice(ctx context.Context, orgId uuid.UUID, deviceName string) error {
	result := s.db.WithContext(ctx).
		Where("org_id = ? AND device_name = ?", orgId, deviceName).
		Delete(&model.EnrollmentHookNotifySecret{})
	return store.ErrorFromGormError(result.Error)
}
