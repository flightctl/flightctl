package store

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Organization interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, org *model.Organization) (*model.Organization, error)
	UpsertMany(ctx context.Context, orgs []*model.Organization) ([]*model.Organization, error)
	List(ctx context.Context) ([]*model.Organization, error)
	ListByExternalIDs(ctx context.Context, externalIDs []string) ([]*model.Organization, error)
	ListByIDs(ctx context.Context, ids []string) ([]*model.Organization, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.Organization, error)
}

type OrganizationStore struct {
	dbHandler *gorm.DB
}

// Ensure OrganizationStore implements the Organization interface
var _ Organization = (*OrganizationStore)(nil)

const externalIDIndex = "org_external_id_idx"

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

	if err := s.createExternalIDIndex(db); err != nil {
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
				ID:          org.DefaultID,
				ExternalID:  org.DefaultExternalID,
				DisplayName: org.DefaultDisplayName,
			}).Error; err != nil {
				return err
			}
		}

		return nil
	})
}

func (s *OrganizationStore) createExternalIDIndex(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&model.Organization{}, externalIDIndex) {
		if db.Dialector.Name() == "postgres" {
			return db.Exec(`
				CREATE UNIQUE INDEX IF NOT EXISTS ` + externalIDIndex + `
				ON organizations (external_id)
				WHERE external_id <> '';
			`).Error
		} else {
			return db.Migrator().CreateIndex(&model.Organization{}, "ExternalID")
		}
	}
	return nil
}

func (s *OrganizationStore) Create(ctx context.Context, org *model.Organization) (*model.Organization, error) {
	db := s.getDB(ctx)

	if org.ID == uuid.Nil {
		org.ID = uuid.New()
	}

	if org.ExternalID == "" {
		return nil, errors.New("external_id is required")
	}

	if org.DisplayName == "" {
		return nil, errors.New("display_name is required")
	}

	if err := db.Create(org).Error; err != nil {
		return nil, err
	}

	return org, nil
}

func (s *OrganizationStore) UpsertMany(ctx context.Context, orgs []*model.Organization) ([]*model.Organization, error) {
	db := s.getDB(ctx)

	if len(orgs) == 0 {
		return orgs, nil
	}

	for _, org := range orgs {
		if org.ID == uuid.Nil {
			org.ID = uuid.New()
		}

		if org.ExternalID == "" {
			return nil, errors.New("external_id is required")
		}

		if org.DisplayName == "" {
			return nil, errors.New("display_name is required")
		}
	}
	// On conflict, do nothing (keep existing record)
	if err := db.Clauses(clause.Expr{
		SQL: "ON CONFLICT (external_id) WHERE external_id <> '' DO NOTHING",
	}).Create(orgs).Error; err != nil {
		return nil, err
	}

	// Now retrieve all the organizations (both newly created and existing ones)
	// by their external IDs to return the actual database records
	externalIDs := make([]string, len(orgs))
	for i, org := range orgs {
		externalIDs[i] = org.ExternalID
	}

	var result []*model.Organization
	if err := db.Where("external_id IN ?", externalIDs).Find(&result).Error; err != nil {
		return nil, err
	}

	return result, nil
}

func (s *OrganizationStore) GetByID(ctx context.Context, id uuid.UUID) (*model.Organization, error) {
	db := s.getDB(ctx)

	var org model.Organization
	result := db.Where("id = ?", id).Take(&org)
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

func (s *OrganizationStore) ListByExternalIDs(ctx context.Context, externalIDs []string) ([]*model.Organization, error) {
	if len(externalIDs) == 0 {
		return []*model.Organization{}, nil
	}

	db := s.getDB(ctx)

	var orgs []*model.Organization
	if err := db.Where("external_id IN ?", externalIDs).Find(&orgs).Error; err != nil {
		return nil, err
	}

	return orgs, nil
}

func (s *OrganizationStore) ListByIDs(ctx context.Context, ids []string) ([]*model.Organization, error) {
	if len(ids) == 0 {
		return []*model.Organization{}, nil
	}

	db := s.getDB(ctx)

	var orgs []*model.Organization
	if err := db.Where("id IN ?", ids).Find(&orgs).Error; err != nil {
		return nil, err
	}

	return orgs, nil
}
