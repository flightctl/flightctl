package store

import (
	"context"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Organization interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, org *model.Organization) (*model.Organization, error)
	List(ctx context.Context) ([]*model.Organization, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.Organization, error)
}

type OrganizationStore struct {
	dbHandler *gorm.DB
}

// Ensure OrganizationStore implements the Organization interface
var _ Organization = (*OrganizationStore)(nil)

func NewOrganization(db *gorm.DB) Organization {
	return &OrganizationStore{dbHandler: db}
}

func (s *OrganizationStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *OrganizationStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)
	if err := db.AutoMigrate(&model.Organization{}); err != nil {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.Organization{}).Count(&count).Error; err != nil {
			return err
		}

		// If there are no organizations, create a default one
		if count == 0 {
			if err := tx.Create(&model.Organization{
				ID:          NullOrgId,
				DisplayName: "Default",
				IsDefault:   true,
			}).Error; err != nil {
				return err
			}
		}

		return nil
	})
}

func (s *OrganizationStore) Create(ctx context.Context, org *model.Organization) (*model.Organization, error) {
	db := s.getDB(ctx)

	if org.ID == uuid.Nil {
		org.ID = uuid.New()
	}

	if err := db.Create(org).Error; err != nil {
		return nil, err
	}

	return org, nil
}

func (s *OrganizationStore) GetByID(ctx context.Context, id uuid.UUID) (*model.Organization, error) {
	db := s.getDB(ctx)

	var org model.Organization
	result := db.Where("id = ?", id).First(&org)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, flterrors.ErrResourceNotFound
		}
		return nil, result.Error
	}

	return &org, nil
}

func (s *OrganizationStore) List(ctx context.Context) ([]*model.Organization, error) {
	db := s.getDB(ctx)

	var orgs []*model.Organization
	if err := db.Find(&orgs).Error; err != nil {
		return nil, err
	}

	return orgs, nil
}
