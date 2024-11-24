package store

import (
	"context"
	"errors"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type TemplateVersion interface {
	Create(ctx context.Context, orgId uuid.UUID, templateVersion *api.TemplateVersion, callback TemplateVersionStoreCallback) (*api.TemplateVersion, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.TemplateVersionList, error)
	DeleteAll(ctx context.Context, orgId uuid.UUID, fleet *string) error
	Get(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*api.TemplateVersion, error)
	Delete(ctx context.Context, orgId uuid.UUID, fleet string, name string) error
	GetLatest(ctx context.Context, orgId uuid.UUID, fleet string) (*api.TemplateVersion, error)
	InitialMigration() error
}

type TemplateVersionStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

type TemplateVersionStoreCallback func(tv *model.TemplateVersion)

// Make sure we conform to TemplateVersion interface
var _ TemplateVersion = (*TemplateVersionStore)(nil)

func NewTemplateVersion(db *gorm.DB, log logrus.FieldLogger) TemplateVersion {
	return &TemplateVersionStore{db: db, log: log}
}

func (s *TemplateVersionStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.TemplateVersion{})
}

func (s *TemplateVersionStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.TemplateVersion, callback TemplateVersionStoreCallback) (*api.TemplateVersion, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}

	templateVersion, err := model.NewTemplateVersionFromApiResource(resource)
	if err != nil {
		return nil, err
	}
	templateVersion.OrgID = orgId
	templateVersion.Generation = lo.ToPtr[int64](1)
	templateVersion.ResourceVersion = lo.ToPtr[int64](1)

	if err = s.db.Create(templateVersion).Error; err != nil {
		return nil, ErrorFromGormError(err)
	}
	callback(templateVersion)
	return lo.ToPtr(templateVersion.ToApiResource()), err
}

func (s *TemplateVersionStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.TemplateVersionList, error) {
	var templateVersions model.TemplateVersionList
	var nextContinue *string
	var numRemaining *int64

	if listParams.Limit < 0 {
		return nil, flterrors.ErrLimitParamOutOfBounds
	}

	query, err := List(&model.TemplateVersion{}).Build(ctx, s.db, orgId, listParams)
	if err != nil {
		return nil, err
	}

	if listParams.Limit > 0 {
		query.Limit(listParams.Limit)
	}

	nextContinueStruct, err := query.Find(ctx, &templateVersions)
	if err != nil {
		return nil, ErrorFromGormError(err)
	}

	if nextContinueStruct != nil {
		contStr := nextContinueStruct.AsString()
		nextContinue = &contStr
		numRemaining = &nextContinueStruct.Count
	}

	apiTemplateVersionList := templateVersions.ToApiResource(nextContinue, numRemaining)
	return &apiTemplateVersionList, nil
}

func (s *TemplateVersionStore) GetLatest(ctx context.Context, orgId uuid.UUID, fleet string) (*api.TemplateVersion, error) {
	var templateVersion model.TemplateVersion
	result := s.db.Model(&templateVersion).Where("org_id = ? AND fleet_name = ?", orgId, fleet).Order("created_at DESC").First(&templateVersion)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	apiResource := templateVersion.ToApiResource()
	return &apiResource, nil
}

func (s *TemplateVersionStore) DeleteAll(ctx context.Context, orgId uuid.UUID, fleet *string) error {
	condition := model.TemplateVersion{}
	unscoped := s.db.Unscoped()
	var whereQuery *gorm.DB
	if fleet != nil {
		whereQuery = unscoped.Where("org_id = ? AND fleet_name = ?", orgId, *fleet)
	} else {
		whereQuery = unscoped.Where("org_id = ?", orgId)
	}

	result := whereQuery.Delete(&condition)
	return ErrorFromGormError(result.Error)
}

func (s *TemplateVersionStore) Get(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*api.TemplateVersion, error) {
	templateVersion := model.TemplateVersion{
		OrgID:     orgId,
		FleetName: fleet,
		Name:      name,
	}
	result := s.db.First(&templateVersion)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	apiTemplateVersion := templateVersion.ToApiResource()
	return &apiTemplateVersion, nil
}

func (s *TemplateVersionStore) Delete(ctx context.Context, orgId uuid.UUID, fleet string, name string) error {
	condition := model.TemplateVersion{
		OrgID:     orgId,
		FleetName: fleet,
		Name:      name,
	}
	result := s.db.Unscoped().Delete(&condition)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil
	}
	return ErrorFromGormError(result.Error)
}

func (s *TemplateVersionStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.TemplateVersion, valid *bool, callback TemplateVersionStoreCallback) error {
	if resource == nil {
		return flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return flterrors.ErrResourceNameIsNil
	}
	_, ownerName, _ := util.GetResourceOwner(resource.Metadata.Owner)
	templateVersion := model.TemplateVersion{
		OrgID:     orgId,
		FleetName: ownerName,
		Name:      *resource.Metadata.Name,
	}

	updates := map[string]interface{}{
		"status":           model.MakeJSONField(resource.Status),
		"resource_version": gorm.Expr("resource_version +1"),
	}
	if valid != nil {
		updates["valid"] = valid
	}

	result := s.db.Model(&templateVersion).Updates(updates)
	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}

	if valid != nil && *valid {
		callback(&templateVersion)
	}
	return nil
}
