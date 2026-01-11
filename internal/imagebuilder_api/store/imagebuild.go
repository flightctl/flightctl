package store

import (
	"context"
	"errors"
	"time"

	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/flterrors"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ImageBuildStore is the store interface for ImageBuild resources
type ImageBuildStore interface {
	Create(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) (*api.ImageBuild, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, error)
	List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (*api.ImageBuildList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) (*api.ImageBuild, error)
	UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error
	InitialMigration(ctx context.Context) error
	// Transaction executes fn within a database transaction, passing the transaction via context
	Transaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// imageBuildStore is the concrete implementation of ImageBuildStore
type imageBuildStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

// NewImageBuildStore creates a new ImageBuild store
func NewImageBuildStore(db *gorm.DB, log logrus.FieldLogger) ImageBuildStore {
	return &imageBuildStore{
		db:  db,
		log: log,
	}
}

// InitialMigration creates the image_builds table
func (s *imageBuildStore) InitialMigration(ctx context.Context) error {
	return s.db.WithContext(ctx).AutoMigrate(&ImageBuild{})
}

// Create creates a new ImageBuild resource
// If a transaction exists in the context (via WithTx), it will be used automatically
func (s *imageBuildStore) Create(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) (*api.ImageBuild, error) {
	if imageBuild == nil || imageBuild.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}

	m, err := NewImageBuildFromApiResource(imageBuild)
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

// Get retrieves an ImageBuild resource by name
// If a transaction exists in the context (via WithTx), it will be used automatically
func (s *imageBuildStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, error) {
	m := &ImageBuild{}
	db := getDB(ctx, s.db)
	result := db.WithContext(ctx).Where("org_id = ? AND name = ?", orgId, name).First(m)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, flterrors.ErrResourceNotFound
		}
		return nil, result.Error
	}

	return m.ToApiResource()
}

// List retrieves a list of ImageBuild resources
func (s *imageBuildStore) List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (*api.ImageBuildList, error) {
	var models []ImageBuild
	var nextContinue *string
	var numRemaining *int64

	// Default to sorting by creation date descending (newest first)
	if len(listParams.SortColumns) == 0 {
		listParams.SortColumns = []flightctlstore.SortColumn{flightctlstore.SortByCreatedAt}
		sortDesc := flightctlstore.SortDesc
		listParams.SortOrder = &sortDesc
	}

	query, err := flightctlstore.ListQuery(&ImageBuild{}).Build(ctx, s.db.WithContext(ctx), orgId, listParams)
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

	list, err := ImageBuildsToApiResource(models, nextContinue, numRemaining)
	if err != nil {
		return nil, err
	}
	return &list, nil
}

// calculateContinue calculates the continue token for pagination
func (s *imageBuildStore) calculateContinue(ctx context.Context, orgId uuid.UUID, models []ImageBuild, listParams flightctlstore.ListParams) (*string, *int64) {
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
		countQuery, err := flightctlstore.ListQuery(&ImageBuild{}).Build(ctx, s.db.WithContext(ctx), orgId, listParams)
		if err == nil {
			numRemainingVal = flightctlstore.CountRemainingItems(countQuery, continueValues, listParams)
		}
	}

	return flightctlstore.BuildContinueString(continueValues, numRemainingVal), &numRemainingVal
}

// Delete removes an ImageBuild resource by name
func (s *imageBuildStore) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	result := s.db.WithContext(ctx).Unscoped().Where("org_id = ? AND name = ?", orgId, name).Delete(&ImageBuild{})
	if result.Error != nil {
		return flightctlstore.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

// UpdateStatus updates the status of an ImageBuild resource
func (s *imageBuildStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) (*api.ImageBuild, error) {
	if imageBuild == nil || imageBuild.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}
	if imageBuild.Status == nil {
		return nil, flterrors.ErrResourceIsNil
	}

	// Update status and return updated row in single query
	var updated []ImageBuild
	result := s.db.WithContext(ctx).Model(&updated).
		Clauses(clause.Returning{}).
		Where("org_id = ? AND name = ?", orgId, *imageBuild.Metadata.Name).
		Update("status", model.MakeJSONField(*imageBuild.Status))
	if result.Error != nil {
		return nil, flightctlstore.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, flterrors.ErrResourceNotFound
	}

	return updated[0].ToApiResource()
}

// UpdateLastSeen updates the last seen timestamp of an ImageBuild resource
func (s *imageBuildStore) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	result := s.db.WithContext(ctx).Model(&ImageBuild{}).
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

// Transaction executes fn within a database transaction, passing the transaction via context
// If a transaction already exists in the context, it will be reused instead of creating a new one
func (s *imageBuildStore) Transaction(ctx context.Context, fn func(ctx context.Context) error) error {
	// Check if a transaction already exists in the context
	if tx := TxFromContext(ctx); tx != nil {
		// Transaction already exists - use it directly
		return fn(ctx)
	}
	// No transaction exists - create a new one
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := WithTx(ctx, tx)
		return fn(txCtx)
	})
}
