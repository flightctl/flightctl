package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GetOption func(*GetOptions)

type GetOptions struct {
	WithExports bool
}

func GetWithExports(val bool) GetOption {
	return func(o *GetOptions) {
		o.WithExports = val
	}
}

type ListOption func(*ListOptions)

type ListOptions struct {
	WithExports bool
}

func ListWithExports(val bool) ListOption {
	return func(o *ListOptions) {
		o.WithExports = val
	}
}

// ImageBuildStore is the store interface for ImageBuild resources
type ImageBuildStore interface {
	Create(ctx context.Context, orgId uuid.UUID, imageBuild *domain.ImageBuild) (*domain.ImageBuild, error)
	Get(ctx context.Context, orgId uuid.UUID, name string, opts ...GetOption) (*domain.ImageBuild, error)
	List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams, opts ...ListOption) (*domain.ImageBuildList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImageBuild, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *domain.ImageBuild) (*domain.ImageBuild, error)
	UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error
	UpdateLogs(ctx context.Context, orgId uuid.UUID, name string, logs string) error
	GetLogs(ctx context.Context, orgId uuid.UUID, name string) (string, error)
	InitialMigration(ctx context.Context) error
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
func (s *imageBuildStore) Create(ctx context.Context, orgId uuid.UUID, imageBuild *domain.ImageBuild) (*domain.ImageBuild, error) {
	if imageBuild == nil || imageBuild.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}

	m, err := NewImageBuildFromDomain(imageBuild)
	if err != nil {
		return nil, err
	}
	m.OrgID = orgId

	// Set initial Generation and ResourceVersion on create (matching GenericStore pattern)
	m.Generation = lo.ToPtr(int64(1))
	m.ResourceVersion = lo.ToPtr(int64(1))

	// Set initial status with Ready=False, Reason=Pending if status is nil or empty
	if m.Status == nil || m.Status.Data.Conditions == nil || len(*m.Status.Data.Conditions) == 0 {
		now := time.Now().UTC()
		initialStatus := domain.ImageBuildStatus{
			Conditions: &[]domain.ImageBuildCondition{
				{
					Type:               domain.ImageBuildConditionTypeReady,
					Status:             domain.ConditionStatusFalse,
					Reason:             string(domain.ImageBuildConditionReasonPending),
					Message:            "ImageBuild created, waiting to be processed",
					LastTransitionTime: now,
				},
			},
		}
		m.Status = model.MakeJSONField(initialStatus)
	}

	db := getDB(ctx, s.db)
	result := db.WithContext(ctx).Create(m)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
			return nil, flterrors.ErrDuplicateName
		}
		return nil, result.Error
	}

	return m.ToDomain()
}

// Get retrieves an ImageBuild resource by name
// If a transaction exists in the context (via WithTx), it will be used automatically
func (s *imageBuildStore) Get(ctx context.Context, orgId uuid.UUID, name string, opts ...GetOption) (*domain.ImageBuild, error) {
	options := GetOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	m := &ImageBuild{}
	db := getDB(ctx, s.db)
	result := db.WithContext(ctx).Where("org_id = ? AND name = ?", orgId, name).First(m)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, flterrors.ErrResourceNotFound
		}
		return nil, result.Error
	}

	var imageExports []domain.ImageExport
	if options.WithExports {
		// Fetch related ImageExports using field selector
		var exportModels []ImageExport
		err := db.WithContext(ctx).
			Where("org_id = ? AND spec->'source'->>'imageBuildRef' = ?", orgId, name).
			Find(&exportModels).Error
		if err == nil {
			// Convert models to domain resources
			for _, exportModel := range exportModels {
				exportDomain, err := exportModel.ToDomain()
				if err == nil {
					imageExports = append(imageExports, *exportDomain)
				}
			}
		}
	}

	var domainOpts []ImageBuildDomainOption
	if len(imageExports) > 0 {
		domainOpts = append(domainOpts, WithImageExports(imageExports))
	}

	return m.ToDomain(domainOpts...)
}

// List retrieves a list of ImageBuild resources
func (s *imageBuildStore) List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams, opts ...ListOption) (*domain.ImageBuildList, error) {
	var models []ImageBuild
	var nextContinue *string
	var numRemaining *int64
	options := ListOptions{}

	for _, opt := range opts {
		opt(&options)
	}

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

	// If withExports is true, fetch ImageExports for each ImageBuild
	var imageExportsMap map[string][]domain.ImageExport
	if options.WithExports && len(models) > 0 {
		// Collect all ImageBuild names
		buildNames := make([]string, len(models))
		for i, build := range models {
			buildNames[i] = build.Name
		}

		// Fetch all ImageExports that reference any of these ImageBuilds
		var exportModels []ImageExport
		err := s.db.WithContext(ctx).
			Where("org_id = ? AND spec->'source'->>'imageBuildRef' IN ?", orgId, buildNames).
			Find(&exportModels).Error
		if err == nil {
			// Group ImageExports by ImageBuild name
			imageExportsMap = make(map[string][]domain.ImageExport)
			for _, exportModel := range exportModels {
				exportDomain, err := exportModel.ToDomain()
				if err == nil {
					// Get the imageBuildRef from the source
					if buildRefSource, err := exportDomain.Spec.Source.AsImageBuildRefSource(); err == nil {
						buildName := buildRefSource.ImageBuildRef
						imageExportsMap[buildName] = append(imageExportsMap[buildName], *exportDomain)
					}
				}
			}
		}
	}

	list, err := ImageBuildsToDomainWithOptions(models, nextContinue, numRemaining, imageExportsMap)
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

// Delete removes an ImageBuild resource by name and returns the deleted resource.
// It also deletes all related ImageExports that reference this ImageBuild.
// Delete is idempotent - returns (nil, nil) if the resource doesn't exist.
func (s *imageBuildStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImageBuild, error) {
	var domainResource *domain.ImageBuild

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Get the resource before deleting it
		m := &ImageBuild{}
		result := tx.Where("org_id = ? AND name = ?", orgId, name).First(m)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				// Idempotent delete - resource doesn't exist, return success
				return nil
			}
			return flightctlstore.ErrorFromGormError(result.Error)
		}

		// Convert to domain resource before deleting
		var err error
		domainResource, err = m.ToDomain()
		if err != nil {
			return err
		}

		// Delete related ImageExports first (cascading delete using owner field)
		owner := util.ResourceOwner(string(domain.ResourceKindImageBuild), name)
		result = tx.Unscoped().Where("org_id = ? AND owner = ?", orgId, owner).Delete(&ImageExport{})
		if result.Error != nil {
			return flightctlstore.ErrorFromGormError(result.Error)
		}

		// Delete the ImageBuild
		result = tx.Unscoped().Where("org_id = ? AND name = ?", orgId, name).Delete(&ImageBuild{})
		if result.Error != nil {
			return flightctlstore.ErrorFromGormError(result.Error)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return domainResource, nil
}

// UpdateStatus updates the status of an ImageBuild resource
func (s *imageBuildStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *domain.ImageBuild) (*domain.ImageBuild, error) {
	if imageBuild == nil || imageBuild.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}
	if imageBuild.Status == nil {
		return nil, flterrors.ErrResourceIsNil
	}

	// Parse resource_version from input for optimistic locking
	var resourceVersion *int64
	if imageBuild.Metadata.ResourceVersion != nil {
		rv, err := strconv.ParseInt(lo.FromPtr(imageBuild.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &rv
	}

	// Update with optional resource_version check for optimistic locking
	// If resourceVersion is nil, skip optimistic locking (no resource_version in WHERE clause)
	// Always increment resource_version regardless
	var updated []ImageBuild
	query := getDB(ctx, s.db).WithContext(ctx).Model(&updated).
		Clauses(clause.Returning{}).
		Where("org_id = ? AND name = ?", orgId, *imageBuild.Metadata.Name)

	if resourceVersion != nil {
		query = query.Where("resource_version = ?", lo.FromPtr(resourceVersion))
	}

	result := query.Updates(map[string]interface{}{
		"status":           model.MakeJSONField(*imageBuild.Status),
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	if result.Error != nil {
		return nil, flightctlstore.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, flterrors.ErrNoRowsUpdated
	}

	return updated[0].ToDomain()
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

// UpdateLogs updates the logs field of an ImageBuild resource
func (s *imageBuildStore) UpdateLogs(ctx context.Context, orgId uuid.UUID, name string, logs string) error {
	result := s.db.WithContext(ctx).Model(&ImageBuild{}).
		Where("org_id = ? AND name = ?", orgId, name).
		Update("logs", logs)
	if result.Error != nil {
		return flightctlstore.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

// GetLogs retrieves the logs field of an ImageBuild resource
func (s *imageBuildStore) GetLogs(ctx context.Context, orgId uuid.UUID, name string) (string, error) {
	var imageBuild ImageBuild
	result := s.db.WithContext(ctx).
		Select("logs").
		Where("org_id = ? AND name = ?", orgId, name).
		First(&imageBuild)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return "", flterrors.ErrResourceNotFound
		}
		return "", flightctlstore.ErrorFromGormError(result.Error)
	}
	if imageBuild.Logs == nil {
		return "", nil
	}
	return *imageBuild.Logs, nil
}
