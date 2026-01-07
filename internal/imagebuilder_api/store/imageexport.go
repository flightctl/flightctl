package store

import (
	"context"
	"errors"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1/imagebuilder"
	"github.com/flightctl/flightctl/internal/flterrors"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ImageExportStore is the store interface for ImageExport resources
type ImageExportStore interface {
	Create(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, error)
	List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (*api.ImageExportList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error)
	UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error
	InitialMigration(ctx context.Context) error
}

// imageExportStore is the concrete implementation of ImageExportStore
type imageExportStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

// NewImageExportStore creates a new ImageExport store
func NewImageExportStore(db *gorm.DB, log logrus.FieldLogger) ImageExportStore {
	return &imageExportStore{
		db:  db,
		log: log,
	}
}

// InitialMigration creates the image_exports table
func (s *imageExportStore) InitialMigration(ctx context.Context) error {
	return s.db.WithContext(ctx).AutoMigrate(&ImageExport{})
}

// Create creates a new ImageExport resource
// If a transaction exists in the context (via WithTx), it will be used automatically
func (s *imageExportStore) Create(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error) {
	if imageExport == nil || imageExport.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}

	m, err := NewImageExportFromApiResource(imageExport)
	if err != nil {
		return nil, err
	}
	m.OrgID = orgId

	db := getDB(ctx, s.db)
	result := db.WithContext(ctx).Create(m)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
			return nil, flterrors.ErrDuplicateName
		}
		return nil, result.Error
	}

	return m.ToApiResource()
}

// Get retrieves an ImageExport resource by name
func (s *imageExportStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, error) {
	m := &ImageExport{}
	result := s.db.WithContext(ctx).Where("org_id = ? AND name = ?", orgId, name).First(m)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, flterrors.ErrResourceNotFound
		}
		return nil, result.Error
	}

	return m.ToApiResource()
}

// List retrieves a list of ImageExport resources
func (s *imageExportStore) List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (*api.ImageExportList, error) {
	var models []ImageExport
	var nextContinue *string
	var numRemaining *int64

	// Default to sorting by creation date descending (newest first)
	if len(listParams.SortColumns) == 0 {
		listParams.SortColumns = []flightctlstore.SortColumn{flightctlstore.SortByCreatedAt}
		sortDesc := flightctlstore.SortDesc
		listParams.SortOrder = &sortDesc
	}

	query, err := flightctlstore.ListQuery(&ImageExport{}).Build(ctx, s.db.WithContext(ctx), orgId, listParams)
	if err != nil {
		return nil, err
	}

	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = flightctlstore.AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue, listParams)
	}

	if err := query.Find(&models).Error; err != nil {
		return nil, flightctlstore.ErrorFromGormError(err)
	}

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(models) > listParams.Limit {
		nextContinue, numRemaining = s.calculateContinue(ctx, orgId, models, listParams)
		models = models[:len(models)-1]
	}

	list, err := ImageExportsToApiResource(models, nextContinue, numRemaining)
	if err != nil {
		return nil, err
	}
	return &list, nil
}

// calculateContinue calculates the continue token for pagination
func (s *imageExportStore) calculateContinue(ctx context.Context, orgId uuid.UUID, models []ImageExport, listParams flightctlstore.ListParams) (*string, *int64) {
	lastIndex := len(models) - 1
	lastItem := models[lastIndex]

	// Build continue value based on sort column (created_at desc by default)
	continueValues := []string{lastItem.CreatedAt.Format(time.RFC3339Nano)}

	var numRemainingVal int64
	if listParams.Continue != nil {
		numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
		if numRemainingVal < 1 {
			numRemainingVal = 1
		}
	} else {
		countQuery, err := flightctlstore.ListQuery(&ImageExport{}).Build(ctx, s.db.WithContext(ctx), orgId, listParams)
		if err == nil {
			numRemainingVal = flightctlstore.CountRemainingItems(countQuery, continueValues, listParams)
		}
	}

	return flightctlstore.BuildContinueString(continueValues, numRemainingVal), &numRemainingVal
}

// Delete removes an ImageExport resource by name
func (s *imageExportStore) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	result := s.db.WithContext(ctx).Unscoped().Where("org_id = ? AND name = ?", orgId, name).Delete(&ImageExport{})
	if result.Error != nil {
		return flightctlstore.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

// UpdateStatus updates the status of an ImageExport resource
func (s *imageExportStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error) {
	if imageExport == nil || imageExport.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}
	if imageExport.Status == nil {
		return nil, flterrors.ErrResourceIsNil
	}

	// Update status and return updated row in single query
	var updated []ImageExport
	result := s.db.WithContext(ctx).Model(&updated).
		Clauses(clause.Returning{}).
		Where("org_id = ? AND name = ?", orgId, *imageExport.Metadata.Name).
		Update("status", model.MakeJSONField(*imageExport.Status))
	if result.Error != nil {
		return nil, flightctlstore.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, flterrors.ErrResourceNotFound
	}

	return updated[0].ToApiResource()
}

// UpdateLastSeen updates the last seen timestamp of an ImageExport resource
func (s *imageExportStore) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	result := s.db.WithContext(ctx).Model(&ImageExport{}).
		Where("org_id = ? AND name = ?", orgId, name).
		Update("status", gorm.Expr("jsonb_set(COALESCE(status, '{}'), '{lastSeen}', to_jsonb(?::text))", timestamp.Format(time.RFC3339)))
	if result.Error != nil {
		return flightctlstore.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return flterrors.ErrResourceNotFound
	}
	return nil
}
