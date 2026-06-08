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
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ImagePromotionStore is the store interface for ImagePromotion resources
type ImagePromotionStore interface {
	Create(ctx context.Context, orgId uuid.UUID, pub *domain.ImagePromotion) (*domain.ImagePromotion, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImagePromotion, error)
	List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (*domain.ImagePromotionList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImagePromotion, error)
	// Update updates the spec, labels, and status of an ImagePromotion atomically.
	// Used for PATCH/PUT operations that add new export formats.
	Update(ctx context.Context, orgId uuid.UUID, pub *domain.ImagePromotion) (*domain.ImagePromotion, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, pub *domain.ImagePromotion) (*domain.ImagePromotion, error)
	// ListPendingForBuild returns all WaitingForArtifacts and AmendmentFailed promotions for the given imageBuildRef.
	// Used to re-evaluate promotions when an ImageBuild or ImageExport status changes.
	ListPendingForBuild(ctx context.Context, orgId uuid.UUID, imageBuildRef string) ([]domain.ImagePromotion, error)
	InitialMigration(ctx context.Context) error
}

// imagePromotionStore is the concrete implementation of ImagePromotionStore
type imagePromotionStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

// NewImagePromotionStore creates a new ImagePromotion store
func NewImagePromotionStore(db *gorm.DB, log logrus.FieldLogger) ImagePromotionStore {
	return &imagePromotionStore{
		db:  db,
		log: log,
	}
}

// InitialMigration creates the image_build_promotions table
func (s *imagePromotionStore) InitialMigration(ctx context.Context) error {
	return s.db.WithContext(ctx).AutoMigrate(&ImagePromotion{})
}

// Create creates a new ImagePromotion resource
func (s *imagePromotionStore) Create(ctx context.Context, orgId uuid.UUID, pub *domain.ImagePromotion) (*domain.ImagePromotion, error) {
	if pub == nil || pub.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}

	m, err := NewImagePromotionFromDomain(pub)
	if err != nil {
		return nil, err
	}
	m.OrgID = orgId

	m.Generation = lo.ToPtr(int64(1))
	m.ResourceVersion = lo.ToPtr(int64(1))

	// Set initial status if not already set
	if m.Status == nil || m.Status.Data.Conditions == nil || len(*m.Status.Data.Conditions) == 0 {
		if pub.Status != nil {
			m.Status = model.MakeJSONField(*pub.Status)
		}
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

// Get retrieves an ImagePromotion resource by name
func (s *imagePromotionStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImagePromotion, error) {
	m := &ImagePromotion{}
	db := getDB(ctx, s.db)
	result := db.WithContext(ctx).Where("org_id = ? AND name = ?", orgId, name).First(m)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, flterrors.ErrResourceNotFound
		}
		return nil, result.Error
	}

	return m.ToDomain()
}

// List retrieves a list of ImagePromotion resources
func (s *imagePromotionStore) List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (*domain.ImagePromotionList, error) {
	var models []ImagePromotion
	var nextContinue *string
	var numRemaining *int64

	if len(listParams.SortColumns) == 0 {
		listParams.SortColumns = []flightctlstore.SortColumn{flightctlstore.SortByCreatedAt, flightctlstore.SortByName}
		sortDesc := flightctlstore.SortDesc
		listParams.SortOrder = &sortDesc
	}

	query, err := flightctlstore.ListQuery(&ImagePromotion{}).Build(ctx, getDB(ctx, s.db).WithContext(ctx), orgId, listParams)
	if err != nil {
		return nil, err
	}

	if listParams.Limit > 0 {
		query = flightctlstore.AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue, listParams)
	}

	if err := query.Find(&models).Error; err != nil {
		return nil, flightctlstore.ErrorFromGormError(err)
	}

	if listParams.Limit > 0 && len(models) > listParams.Limit {
		nextContinue, numRemaining = s.calculateContinue(ctx, orgId, models, listParams)
		models = models[:len(models)-1]
	}

	list, err := ImagePromotionsToDomain(models, nextContinue, numRemaining)
	if err != nil {
		return nil, err
	}
	return &list, nil
}

func (s *imagePromotionStore) calculateContinue(ctx context.Context, orgId uuid.UUID, models []ImagePromotion, listParams flightctlstore.ListParams) (*string, *int64) {
	lastIndex := len(models) - 1
	lastItem := models[lastIndex]

	continueValues := []string{lastItem.CreatedAt.Format(time.RFC3339Nano), lastItem.Name}

	var numRemainingVal int64
	if listParams.Continue != nil {
		numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
		if numRemainingVal < 1 {
			numRemainingVal = 1
		}
	} else {
		countQuery, err := flightctlstore.ListQuery(&ImagePromotion{}).Build(ctx, getDB(ctx, s.db).WithContext(ctx), orgId, listParams)
		if err == nil {
			numRemainingVal = flightctlstore.CountRemainingItems(countQuery, continueValues, listParams)
		}
	}

	return flightctlstore.BuildContinueString(continueValues, numRemainingVal), &numRemainingVal
}

// Delete removes an ImagePromotion resource by name
func (s *imagePromotionStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (*domain.ImagePromotion, error) {
	m := &ImagePromotion{}
	db := getDB(ctx, s.db)
	result := db.WithContext(ctx).Where("org_id = ? AND name = ?", orgId, name).First(m)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, flightctlstore.ErrorFromGormError(result.Error)
	}

	domainResource, err := m.ToDomain()
	if err != nil {
		return nil, err
	}

	result = db.WithContext(ctx).Unscoped().Where("org_id = ? AND name = ?", orgId, name).Delete(&ImagePromotion{})
	if result.Error != nil {
		return nil, flightctlstore.ErrorFromGormError(result.Error)
	}

	return domainResource, nil
}

// Update updates the spec, labels, and status of an ImagePromotion atomically.
// Used for PATCH/PUT operations that add new export formats.
func (s *imagePromotionStore) Update(ctx context.Context, orgId uuid.UUID, pub *domain.ImagePromotion) (*domain.ImagePromotion, error) {
	if pub == nil || pub.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}

	m, err := NewImagePromotionFromDomain(pub)
	if err != nil {
		return nil, err
	}

	var resourceVersion *int64
	if pub.Metadata.ResourceVersion != nil {
		rv, err := strconv.ParseInt(lo.FromPtr(pub.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &rv
	}

	updates := map[string]interface{}{
		"spec":             m.Spec,
		"labels":           m.Labels,
		"resource_version": gorm.Expr("resource_version + 1"),
	}
	if pub.Status != nil {
		updates["status"] = model.MakeJSONField(*pub.Status)
	}

	var updated []ImagePromotion
	query := getDB(ctx, s.db).WithContext(ctx).Model(&updated).
		Clauses(clause.Returning{}).
		Where("org_id = ? AND name = ?", orgId, *pub.Metadata.Name)

	if resourceVersion != nil {
		query = query.Where("resource_version = ?", lo.FromPtr(resourceVersion))
	}

	result := query.Updates(updates)
	if result.Error != nil {
		return nil, flightctlstore.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, flterrors.ErrNoRowsUpdated
	}

	return updated[0].ToDomain()
}

// UpdateStatus updates the status of an ImagePromotion resource
func (s *imagePromotionStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, pub *domain.ImagePromotion) (*domain.ImagePromotion, error) {
	if pub == nil || pub.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}
	if pub.Status == nil {
		return nil, flterrors.ErrResourceIsNil
	}

	var resourceVersion *int64
	if pub.Metadata.ResourceVersion != nil {
		rv, err := strconv.ParseInt(lo.FromPtr(pub.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &rv
	}

	var updated []ImagePromotion
	query := getDB(ctx, s.db).WithContext(ctx).Model(&updated).
		Clauses(clause.Returning{}).
		Where("org_id = ? AND name = ?", orgId, *pub.Metadata.Name)

	if resourceVersion != nil {
		query = query.Where("resource_version = ?", lo.FromPtr(resourceVersion))
	}

	result := query.Updates(map[string]interface{}{
		"status":           model.MakeJSONField(*pub.Status),
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

// ListPendingForBuild returns all WaitingForArtifacts and AmendmentFailed promotions for a given imageBuildRef in the org.
// Used to re-evaluate promotions when an ImageBuild or ImageExport status changes.
func (s *imagePromotionStore) ListPendingForBuild(ctx context.Context, orgId uuid.UUID, imageBuildRef string) ([]domain.ImagePromotion, error) {
	var models []ImagePromotion
	result := getDB(ctx, s.db).WithContext(ctx).
		Where(
			"org_id = ? AND spec->'source'->>'imageBuildRef' = ? AND (SELECT elem->>'reason' FROM jsonb_array_elements(COALESCE(status, '{}'::jsonb)->'conditions') AS elem WHERE elem->>'type' = 'Ready' LIMIT 1) IN ?",
			orgId, imageBuildRef, []string{
				string(domain.ImagePromotionConditionReasonWaitingForArtifacts),
				string(domain.ImagePromotionConditionReasonAmendmentFailed),
			},
		).
		Find(&models)
	if result.Error != nil {
		return nil, flightctlstore.ErrorFromGormError(result.Error)
	}

	pubs := make([]domain.ImagePromotion, 0, len(models))
	for i := range models {
		d, err := models[i].ToDomain()
		if err != nil {
			return nil, err
		}
		pubs = append(pubs, *d)
	}
	return pubs, nil
}
