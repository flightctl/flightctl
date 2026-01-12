package store

import (
	"context"
	"errors"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1/imagebuilder"
	"github.com/flightctl/flightctl/internal/flterrors"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// ImagePipelineStore provides transaction support for atomic operations and JOIN queries
type ImagePipelineStore interface {
	// Transaction executes fn within a database transaction, passing the transaction via context
	Transaction(ctx context.Context, fn func(ctx context.Context) error) error
	// Get retrieves an ImageBuild with all associated ImageExports using a JOIN query
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, []api.ImageExport, error)
	// List retrieves ImageBuilds with their associated ImageExports using JOIN queries
	List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) ([]ImageBuildWithExports, *string, *int64, error)
}

// ImageBuildWithExports represents an ImageBuild with its associated ImageExports
type ImageBuildWithExports struct {
	ImageBuild   *api.ImageBuild
	ImageExports []api.ImageExport
}

// imagePipelineStore is the concrete implementation
type imagePipelineStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

// NewImagePipelineStore creates a new ImagePipelineStore
func NewImagePipelineStore(db *gorm.DB, log logrus.FieldLogger) ImagePipelineStore {
	return &imagePipelineStore{
		db:  db,
		log: log,
	}
}

// Transaction executes fn within a database transaction, passing the transaction via context
func (s *imagePipelineStore) Transaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := WithTx(ctx, tx)
		return fn(txCtx)
	})
}

// Get retrieves an ImageBuild with all associated ImageExports using a JOIN query
func (s *imagePipelineStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, []api.ImageExport, error) {
	// Get the ImageBuild
	var buildModel ImageBuild
	result := s.db.WithContext(ctx).Where("org_id = ? AND name = ?", orgId, name).First(&buildModel)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil, flterrors.ErrResourceNotFound
		}
		return nil, nil, result.Error
	}

	imageBuild, err := buildModel.ToApiResource()
	if err != nil {
		return nil, nil, err
	}

	// Get all ImageExports that reference this ImageBuild using JSONB query
	var exportModels []ImageExport
	// Query ImageExports where spec->'source'->>'type' = 'imageBuild' AND spec->'source'->>'imageBuildRef' = name
	result = s.db.WithContext(ctx).
		Where("org_id = ?", orgId).
		Where("spec->'source'->>'type' = ?", "imageBuild").
		Where("spec->'source'->>'imageBuildRef' = ?", name).
		Find(&exportModels)
	if result.Error != nil {
		return nil, nil, result.Error
	}

	imageExports := make([]api.ImageExport, 0, len(exportModels))
	for i := range exportModels {
		export, err := exportModels[i].ToApiResource()
		if err != nil {
			return nil, nil, err
		}
		imageExports = append(imageExports, *export)
	}

	return imageBuild, imageExports, nil
}

// List retrieves ImageBuilds with their associated ImageExports using JOIN queries
func (s *imagePipelineStore) List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) ([]ImageBuildWithExports, *string, *int64, error) {
	var builds []ImageBuild
	var nextContinue *string
	var numRemaining *int64

	// Default to sorting by creation date descending (newest first)
	if len(listParams.SortColumns) == 0 {
		listParams.SortColumns = []flightctlstore.SortColumn{flightctlstore.SortByCreatedAt}
		sortDesc := flightctlstore.SortDesc
		listParams.SortOrder = &sortDesc
	}

	// Build the base query for ImageBuilds
	buildQuery, err := flightctlstore.ListQuery(&ImageBuild{}).Build(ctx, s.db.WithContext(ctx), orgId, listParams)
	if err != nil {
		return nil, nil, nil, err
	}

	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		buildQuery = flightctlstore.AddPaginationToQuery(buildQuery, listParams.Limit+1, listParams.Continue, listParams)
	}

	if err := buildQuery.Find(&builds).Error; err != nil {
		return nil, nil, nil, flightctlstore.ErrorFromGormError(err)
	}

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(builds) > listParams.Limit {
		nextContinue, numRemaining = s.calculateContinue(ctx, orgId, builds, listParams)
		builds = builds[:len(builds)-1]
	}

	// Convert builds to API resources and get their associated exports
	result := make([]ImageBuildWithExports, 0, len(builds))
	buildNames := make([]string, 0, len(builds))
	buildMap := make(map[string]*api.ImageBuild)

	for i := range builds {
		build, err := builds[i].ToApiResource()
		if err != nil {
			return nil, nil, nil, err
		}
		buildName := lo.FromPtr(build.Metadata.Name)
		buildNames = append(buildNames, buildName)
		buildMap[buildName] = build
	}

	// Get all ImageExports that reference any of these ImageBuilds in a single query
	exportsByBuild := make(map[string][]api.ImageExport)
	if len(buildNames) > 0 {
		var exportModels []ImageExport
		// Use GORM Where clauses with JSONB operators and IN clause
		queryResult := s.db.WithContext(ctx).
			Where("org_id = ?", orgId).
			Where("spec->'source'->>'type' = ?", "imageBuild").
			Where("spec->'source'->>'imageBuildRef' IN ?", buildNames).
			Find(&exportModels)
		if queryResult.Error != nil {
			return nil, nil, nil, flightctlstore.ErrorFromGormError(queryResult.Error)
		}

		// Group exports by ImageBuild name
		for i := range exportModels {
			export, err := exportModels[i].ToApiResource()
			if err != nil {
				return nil, nil, nil, err
			}
			sourceType, err := export.Spec.Source.Discriminator()
			if err != nil || sourceType != string(api.ImageExportSourceTypeImageBuild) {
				continue
			}
			source, err := export.Spec.Source.AsImageBuildRefSource()
			if err != nil {
				continue
			}
			exportsByBuild[source.ImageBuildRef] = append(exportsByBuild[source.ImageBuildRef], *export)
		}
	}

	// Build result with exports grouped by build
	for _, buildName := range buildNames {
		build := buildMap[buildName]
		exports := exportsByBuild[buildName]
		if exports == nil {
			exports = []api.ImageExport{}
		}
		result = append(result, ImageBuildWithExports{
			ImageBuild:   build,
			ImageExports: exports,
		})
	}

	return result, nextContinue, numRemaining, nil
}

// calculateContinue calculates the continue token for pagination
func (s *imagePipelineStore) calculateContinue(ctx context.Context, orgId uuid.UUID, builds []ImageBuild, listParams flightctlstore.ListParams) (*string, *int64) {
	lastIndex := len(builds) - 1
	lastItem := builds[lastIndex]

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
